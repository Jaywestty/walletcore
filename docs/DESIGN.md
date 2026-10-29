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
