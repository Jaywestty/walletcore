# Design decisions and trade-offs

This document records why WalletCore is built the way it is. It grows with each
phase. The "Database guarantees" section is complete as of Phase 2.

## Database guarantees

The database is the last line of defence. The application does the ordering,
locking and arithmetic, but nothing that reaches COMMIT can leave the ledger
structurally wrong. Each guarantee below is enforced by a database mechanism and
fails with a stable SQLSTATE that the Go layer maps to a sentinel error. Code
is checked against the SQLSTATE, never against message text.

### Custom SQLSTATE codes

| SQLSTATE | Meaning |
|---|---|
| LD001 | Journal is unbalanced, or has fewer than two entries. |
| LD002 | An entry currency does not match its account currency. |
| LD003 | A balance would go negative on an account kind that forbids it. |
| LD004 | An immutable row or table was updated, deleted or truncated. |
| LD005 | An entry was written outside the transaction that created its journal. |

Standard PostgreSQL SQLSTATEs still apply to plain constraints and foreign keys.
Those are mapped in Go from the exact constraint name, never from message text.
The names below are written explicitly in the migration so they are stable.

| SQLSTATE | Constraint name | Condition |
|---|---|---|
| 23505 | `ledger_journals_idempotency_key_uniq` | Duplicate idempotency key. |
| 23505 | `ledger_journals_reverses_uniq` | A journal reversed a second time. |
| 23505 | `ledger_accounts_kind_owner_currency_uniq` | Duplicate account for the same kind, owner and currency. |
| 23514 | `ledger_entries_amount_positive_check` | Entry amount is not strictly positive. |
| 23514 | `ledger_journals_reversal_pairing_check` | Reversal pairing is wrong for the journal kind. |
| 23514 | `ledger_journals_kind_check` | Journal kind is not in the allowed set. |
| 23503 | (foreign key) | An entry references a journal that does not exist. |

### Guarantees

| Guarantee | Mechanism | SQLSTATE |
|---|---|---|
| A journal has at least two entries. | Deferred constraint trigger on `ledger_journals` `AFTER INSERT`; counts entries at COMMIT. | LD001 |
| Debits equal credits within each currency of a journal. | Same deferred trigger; groups entries by currency and compares sums. | LD001 |
| An entry's currency equals its account's currency. | Same deferred trigger; joins entries to accounts and counts mismatches. | LD002 |
| A journal committed with zero entries is rejected. | The check is attached to the journal, so it runs even when no entry was inserted. | LD001 |
| Entries are only written in the transaction that created their journal. | `tx_id` on the journal plus a `BEFORE INSERT` trigger on entries comparing it with `txid_current()`. | LD005 |
| An entry cannot reference a journal that does not exist. | The same entry trigger reports a missing journal as a foreign key violation. | 23503 |
| `user_wallet`, `holds` and `fees` balances never go negative. | `BEFORE INSERT OR UPDATE` trigger on `ledger_balances` that reads the account kind and its `allow_negative` flag. | LD003 |
| `settlement` may go negative. | Its kind row has `allow_negative = TRUE`, so the same trigger permits it. | n/a |
| Journals cannot be updated or deleted. | `BEFORE UPDATE OR DELETE` row triggers. | LD004 |
| Journals cannot be truncated. | `BEFORE TRUNCATE` statement trigger. When the table is the parent of a foreign key, PostgreSQL rejects the truncate first with `0A000`; the outcome is the same. | LD004 |
| Entries cannot be updated or deleted. | `BEFORE UPDATE OR DELETE` row triggers. | LD004 |
| Entries cannot be truncated. | `BEFORE TRUNCATE` statement trigger; same foreign-key note as journals. | LD004 |
| The balance projection can be updated but never deleted or truncated. | `BEFORE DELETE` row trigger and `BEFORE TRUNCATE` statement trigger; `UPDATE` is left open because that is the projection's purpose. | LD004 |
| Accounts cannot be deleted. | `BEFORE DELETE` row trigger. | LD004 |
| An account's kind, owner and currency never change. | `BEFORE UPDATE` row trigger comparing old and new values. | LD004 |
| Account kinds never change, are never deleted and are never truncated. | `BEFORE UPDATE OR DELETE` row trigger and `BEFORE TRUNCATE` statement trigger on `ledger_account_kinds`. | LD004 |
| Exactly one account exists per kind, owner and currency. | Named unique index `ledger_accounts_kind_owner_currency_uniq`. | 23505 |
| An idempotency key is used at most once. | Named unique constraint `ledger_journals_idempotency_key_uniq`. | 23505 |
| A journal is reversed at most once. | Partial unique index `ledger_journals_reverses_uniq` where `reverses_journal_id` is not null. | 23505 |
| Only a reversal journal references an original, and every reversal references one. | Named CHECK `ledger_journals_reversal_pairing_check`. | 23514 |
| Entry amounts are strictly positive. | Named CHECK `ledger_entries_amount_positive_check`. | 23514 |
| Journal kinds are from a fixed set. | Named CHECK `ledger_journals_kind_check`. | 23514 |

### Why the balance check is attached to the journal, not the entry

A constraint trigger on `ledger_entries` only runs when an entry is inserted.
A journal inserted with no entries would never be checked. Attaching the
deferred check to `ledger_journals` `AFTER INSERT` means every inserted journal
is verified at COMMIT regardless of how many entries it has, and the check runs
once per journal instead of once per entry.

### Why `tx_id` and the entry transaction guard

A journal and its entries must be inserted together so the deferred balance
check can see the full journal. Without a guard, an unrelated later transaction
could append entries to an already committed journal, which the deferred check
would then accept if the appended entries happened to balance. `tx_id` records
the inserting transaction, and the entry trigger rejects any entry whose
journal was created in a different transaction. This closes that hole and makes
posting atomic by construction.

### Deferred constraint triggers and COMMIT

A `DEFERRABLE INITIALLY DEFERRED` constraint trigger does not fire when the row
is inserted. It queues its check until the transaction commits, after all the
transaction's changes are visible to the check. In Go this means the individual
`Exec` calls for the journal and its entries succeed, and the balance or
currency error surfaces on `tx.Commit()`. The posting engine must treat a
commit error carrying LD001 or LD002 as a domain error, not a transport
failure, and must not retry it in a new transaction.

## Ledger posting and balance convention

The posting engine is Go code in `internal/ledger`, not a database function.
The database is only the safety net described above. A single call to
`Repository.PostTransaction` runs one explicit transaction and does the
following in a fixed order:

1. Validate the request and compute a fingerprint (pure, no database).
2. Resolve every account, creating it and its zero balance if missing.
3. Lock the affected balance rows with `SELECT ... FOR UPDATE` ordered by
   account id ascending, in one query.
4. Compute the new balances from the locked values and update them.
5. Insert the journal, then its entries, in the same transaction.
6. Commit. LD001 and LD002 surface here.

### Balance sign convention

The cached `ledger_balances.balance` is stored normal-side positive for every
account kind. For `user_wallet`, `holds` and `fees` (credit normal) the balance
is credits minus debits. For `settlement` (debit normal) the balance is debits
minus credits. A positive balance therefore always means the account holds
value on its normal side, and only `settlement` may go negative.

The engine computes the change for each leg with one pure function,
`Delta(kind, side, amount)`: it returns `+amount` when the side equals the
kind's normal side and `-amount` otherwise.

### Why locks are ordered by account id

Concurrent postings that touch overlapping accounts must acquire row locks in
the same order, or they can deadlock. Locking every affected balance in one
`ORDER BY account_id ... FOR UPDATE` statement guarantees a global order, so two
transactions can never hold one lock and wait for the other.

### Amount limits and overflow

A single leg may not exceed `MaxAmount`, which is 1,000,000,000,000 minor units
(one trillion). The value is well inside int64 and keeps every consumer able to
represent an amount without special handling. Beyond that bound, callers are
almost certainly sending the wrong unit.

Even with that bound, per-currency totals and balance updates use checked
addition. With at most twenty legs the largest possible total is 2e13, so it
cannot overflow today, but performing the addition through a checked helper
means a future change to the leg count or the amount bound cannot introduce a
silent wrap. A per-currency sum or a balance that would overflow returns
`ErrInvalidJournal`; a single leg above `MaxAmount` returns `ErrInvalidAmount`.

## Concurrency and idempotency

Every posting acquires locks in one fixed order, with no exceptions:

1. A transaction-scoped advisory lock on the idempotency key.
2. The referenced journal row with `FOR UPDATE`: the original for a reversal, or
   the withdrawal request for a settlement.
3. Accounts, resolved in sorted `refKey` order.
4. Balance rows with `FOR UPDATE`, ordered by account id ascending.

Step 2 covers one referenced journal, whether the posting reverses it or settles
it. A journal is either reversed or settled, never both, and a settlement
targets a `withdrawal_request`, which is never reversed, so the two cases never
touch the same row in one posting and add no new lock edges.

Because every path requests these resources in the same total order, a wait-for
cycle is impossible: transaction A can only wait on a resource that transaction
B took, and B took it only at a step earlier than or equal to A's current step
in the same order, so a cycle back to A cannot close. This is the same argument
as locking accounts in id order, extended to the key lock and the referenced
journal.

### Idempotency

The posting engine takes `pg_advisory_xact_lock(hashtextextended(key, 0))` at the
start of the transaction, then loads any journal with that key.

- Same key and same fingerprint: the original journal is returned with its
  entries and `Replayed = true`. A replay performs no balance work, so it
  succeeds even if accounts are now suspended or funds are now too low. The
  balances reported on a replay are the current values for the touched
  accounts, not the historical values at original posting time.
- Same key and different fingerprint: `ErrIdempotencyConflict`. Any difference
  in kind, legs, actor, reason or reversal target is a conflict. Metadata and
  occurrence time are not part of the fingerprint and may differ freely.
- No journal found: the normal posting path runs. The unique index on the key
  remains the hard guarantee. A `23505` on the key after acquiring the advisory
  lock would indicate a bug and is mapped to `ErrIdempotencyConflict`.

The advisory lock serializes same-key retries before any row lock is taken, and
under READ COMMITTED each statement sees the latest committed snapshot, so a
retry that waited on the lock then re-reads the row the first attempt committed.

### Commit failure classification

A rejection from the database before `COMMIT` is a definite failure: nothing was
persisted. A `COMMIT` that returns a database rejection (a constraint, a trigger,
or the ledger's own SQLSTATE class) is also definite: the transaction did not
commit. Any other commit error, such as a context deadline, a closed connection
or SQLSTATE class 08 or 57P01, is ambiguous: the commit may or may not have been
durable. Such a case returns `ErrOutcomeUnknown` and the caller must resolve it
by retrying with the same idempotency key, which will either replay the committed
journal or post it once.

Lock and concurrency SQLSTATEs are classified for the caller:
`55P03` maps to `ErrLockTimeout`, `40P01` to `ErrDeadlockDetected` and `40001`
to `ErrSerializationFailure`. Only the last two are retried automatically,
within a bounded attempt budget; `ErrOutcomeUnknown` is never retried inside the
engine because the caller owns that decision.

## Known limitations

These are accepted trade-offs, not bugs, and each has a mitigation.

### Acknowledgment-lost ambiguous commit

If the connection fails while the server is processing COMMIT, the client sees
`ErrOutcomeUnknown`: the commit may or may not be durable. The engine does not
retry this case itself because a second write could double-post if the first
did commit. The caller resolves it by retrying the same request with the same
idempotency key. That retry either replays the committed journal or posts it
once, so stability comes from the key, not from the transport.

### Advisory lock hash collisions

The idempotency serialization uses `pg_advisory_xact_lock(hashtextextended(key,
0))`, a 64-bit hash of the key. Two different keys can hash to the same value.
When that happens the two postings serialize unnecessarily but remain correct:
they touch different journals, and the lock only slows them. Collisions become
more likely with a very large number of concurrent distinct keys; the cost is
extra waiting, never incorrect data.

### Cached balances read without a lock on replay

A replay returns the current balances for the touched accounts by reading the
projection without taking `FOR UPDATE`. Because the replay does no balance work
and only reports state, a concurrent posting may change those balances during
the read. The values returned are therefore a recent, not a frozen, snapshot.
Callers that need a strictly consistent read of balances should query them
after their own write, not rely on the balances embedded in a replay result.

## Wallet operations

The wallet service composes balanced journals over the ledger core. It never
writes balances; it only builds legs and calls `PostTransaction`. Every
operation takes an idempotency key, an actor and the owner references, and
posts one journal.

| Operation | Journal kind | Legs | Failure modes |
|---|---|---|---|
| Fund | fund | Dr settlement:core, Cr user_wallet:owner | invalid request, invalid amount |
| Transfer | transfer | Dr user_wallet:from (amount + fee), Cr user_wallet:to (amount), Cr fees:core (fee, only when fee > 0) | insufficient funds, self transfer, invalid amount |
| Charge fee | fee | Dr user_wallet:owner, Cr fees:core | insufficient funds, invalid amount |
| Request withdrawal | withdrawal_request | Dr user_wallet:owner, Cr holds:owner | insufficient funds |
| Finalize withdrawal | withdrawal_finalize | Dr holds:owner, Cr settlement:core, settles_journal_id set | already settled, target not a request |
| Fail withdrawal | withdrawal_fail | Dr holds:owner, Cr user_wallet:owner, settles_journal_id set | already settled, target not a request |
| Reverse | reversal | mirror of the original with opposite sides, reverses_journal_id set | not reversible, already reversed, insufficient funds |

The settlement and fee accounts use the fixed owner reference `core`.

### Withdrawal state machine

A withdrawal is requested, then resolved exactly once:

1. Request. `RequestWithdrawal` moves the amount from the user's wallet into
   their holds account. The journal is a `withdrawal_request`.
2. Settlement. Either `FinalizeWithdrawal` (holds to settlement) or
   `FailWithdrawal` (holds back to the wallet) resolves it. The settlement
   journal records the request in `settles_journal_id`.

A request can be settled at most once, enforced by the partial unique index
`ledger_journals_settles_uniq`. A second finalize or fail, in any combination,
fails with `ErrWithdrawalAlreadySettled`. The state is derived from which
settlement journal exists, never from a mutable status column.

### Reversal rules

Reversal posts a compensating journal and never edits history. Only `fund`,
`transfer` and `fee` journals may be reversed. Withdrawal journals
(`withdrawal_request`, `withdrawal_finalize`, `withdrawal_fail`) are resolved by
finalize or fail and never reversed, so reversing one returns
`ErrInvalidJournal`. A journal is reversed at most once, enforced by the
partial unique index on `reverses_journal_id`; a second attempt returns
`ErrAlreadyReversed`. Reversing a reversal is rejected with `ErrInvalidJournal`.

### Why the state machine is journals plus a unique index

Expressing the withdrawal lifecycle as immutable journals plus a unique index,
rather than a mutable `status` column, keeps the audit trail authoritative. The
allowed transitions are encoded by which journal kinds may reference a request
and by a single uniqueness rule, so there is no separate state to drift out of
sync with the money. The database enforces "settled at most once" even under
concurrent settlement attempts, because only one insert can satisfy the unique
index.

### Journal shape validation

Before posting, the engine checks that the legs match the kind's expected
shape. This runs after the per-currency balance check, so an unbalanced journal
is reported as unbalanced before its shape is considered. Kinds that depend on
a target journal get only a structural check here; their exact legs are checked
against the target inside the transaction.

| Kind | Legs | Notes |
|---|---|---|
| fund | Dr settlement:core, Cr user_wallet | Exactly two legs, equal amount and currency. |
| transfer | Dr user_wallet (amount + fee), Cr user_wallet (amount), optional Cr fees:core (fee) | Two or three legs, one currency, the two wallets differ. |
| fee | Dr user_wallet, Cr fees:core | Exactly two legs, equal amount. |
| withdrawal_request | Dr user_wallet:X, Cr holds:X | Exactly two legs, same owner, equal amount. |
| withdrawal_finalize, withdrawal_fail | exactly two legs | Leg details checked against the request in verifySettlement. |
| reversal | any | Exactly mirrors the original, checked in verifyReversal. |

Settlement and fee accounts always use owner `core`. A leg with a different
shape is rejected with `ErrInvalidJournal` before any database work.
