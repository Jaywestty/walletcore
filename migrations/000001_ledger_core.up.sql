-- WalletCore ledger core.
--
-- Money is integer minor units (BIGINT). No floats in the ledger path.
-- The database is the safety net: it enforces structural correctness
-- (balanced commits, immutability, non-negative balances where required)
-- while the application owns ordering, locking and computation.
--
-- Custom SQLSTATE codes. Every trigger that can reject a committed change
-- raises one of these, one per condition. Application code maps them to
-- sentinel errors by code, never by message text.
--
--   LD001  journal unbalanced, or fewer than two entries
--   LD002  entry currency does not match its account currency
--   LD003  balance would go negative on a kind that forbids it
--   LD004  immutable row or table was updated, deleted or truncated
--   LD005  entry written outside the transaction that created its journal
--
-- These are the codes the schema raises. Plain constraint and foreign key
-- violations keep their standard PostgreSQL SQLSTATEs and are mapped in Go
-- from the constraint name.

-- Account kinds are a small fixed set. Text plus CHECK is used instead of a
-- native ENUM so the allowed values can be widened in a later migration
-- without ALTER TYPE restrictions.
CREATE TABLE ledger_account_kinds (
    kind          TEXT PRIMARY KEY,
    normal_side   TEXT NOT NULL CHECK (normal_side IN ('debit', 'credit')),
    allow_negative BOOLEAN NOT NULL
);

INSERT INTO ledger_account_kinds (kind, normal_side, allow_negative) VALUES
    ('user_wallet', 'credit', FALSE),
    ('holds',       'credit', FALSE),
    ('fees',        'credit', FALSE),
    ('settlement',  'debit',  TRUE);

-- One row per real account, created lazily by the posting engine. An account
-- has exactly one currency. owner_ref identifies the owner inside the system.
CREATE TABLE ledger_accounts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT NOT NULL REFERENCES ledger_account_kinds (kind),
    owner_ref   TEXT NOT NULL CHECK (owner_ref <> ''),
    currency    CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'closed')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Exactly one account per kind, owner and currency. The index is named so the
-- Go error mapper can identify a duplicate-account violation by its name.
CREATE UNIQUE INDEX ledger_accounts_kind_owner_currency_uniq
    ON ledger_accounts (kind, owner_ref, currency);

-- Journals are the transaction header: one balanced movement.
--
-- tx_id records the transaction that inserted the journal. Entries may only be
-- written in that same transaction, which is checked by a trigger on
-- ledger_entries (LD005). This stops entries being appended to a journal after
-- it was committed.
--
-- Constraints are named explicitly so the Go error mapper reads a stable
-- ConstraintName rather than a generated one.
CREATE TABLE ledger_journals (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                TEXT NOT NULL
                            CONSTRAINT ledger_journals_kind_check
                            CHECK (kind IN (
                                'fund', 'transfer', 'withdrawal_request',
                                'withdrawal_finalize', 'withdrawal_fail', 'fee', 'reversal')),
    idempotency_key     TEXT NOT NULL
                            CONSTRAINT ledger_journals_idempotency_key_check
                            CHECK (char_length(idempotency_key) BETWEEN 1 AND 200)
                            CONSTRAINT ledger_journals_idempotency_key_uniq UNIQUE,
    fingerprint         TEXT NOT NULL,
    actor               TEXT NOT NULL CHECK (actor <> ''),
    reason              TEXT NOT NULL CHECK (reason <> ''),
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    reverses_journal_id UUID REFERENCES ledger_journals (id),
    tx_id               BIGINT NOT NULL DEFAULT txid_current(),
    occurred_at         TIMESTAMPTZ NOT NULL,
    posted_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A reversal journal must target an original, and only a reversal may.
    CONSTRAINT ledger_journals_reversal_pairing_check
        CHECK ((kind = 'reversal') = (reverses_journal_id IS NOT NULL))
);

-- A journal can be reversed at most once.
CREATE UNIQUE INDEX ledger_journals_reverses_uniq
    ON ledger_journals (reverses_journal_id)
    WHERE reverses_journal_id IS NOT NULL;

-- Entries are the legs. Each stores a positive amount and a side.
--
-- entry_seq is a monotonic per-row sequence used as a stable tie-breaker when
-- ordering entries for one account, since created_at can collide within a
-- transaction that posts many entries.
CREATE TABLE ledger_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_seq   BIGINT GENERATED ALWAYS AS IDENTITY,
    journal_id  UUID NOT NULL REFERENCES ledger_journals (id),
    account_id  UUID NOT NULL REFERENCES ledger_accounts (id),
    side        TEXT NOT NULL CHECK (side IN ('Dr', 'Cr')),
    amount      BIGINT NOT NULL CONSTRAINT ledger_entries_amount_positive_check CHECK (amount > 0),
    currency    CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entries_account_seq_idx ON ledger_entries (account_id, entry_seq);
CREATE INDEX ledger_entries_journal_idx ON ledger_entries (journal_id);

-- Cached balance projection, one row per account, written in the same
-- transaction as the entries that change it.
CREATE TABLE ledger_balances (
    account_id  UUID PRIMARY KEY REFERENCES ledger_accounts (id),
    balance     BIGINT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Kind-dependent non-negative rule.
--
-- A plain CHECK on ledger_balances cannot see the account kind. The rule is
-- applied with a trigger that runs on INSERT and UPDATE of the projection and
-- looks up the account kind, allowing a negative balance only when the kind
-- has allow_negative = TRUE. The lookup is a small read against a table that
-- never changes after seeding, so it is cheap.
CREATE FUNCTION ledger_balances_check_non_negative()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_allow_negative BOOLEAN;
BEGIN
    SELECT k.allow_negative INTO v_allow_negative
    FROM ledger_accounts a
    JOIN ledger_account_kinds k ON k.kind = a.kind
    WHERE a.id = NEW.account_id;

    IF NEW.balance < 0 AND v_allow_negative IS NOT TRUE THEN
        RAISE EXCEPTION 'account % kind does not allow negative balances', NEW.account_id
            USING ERRCODE = 'LD003';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_ledger_balances_non_negative
    BEFORE INSERT OR UPDATE ON ledger_balances
    FOR EACH ROW EXECUTE FUNCTION ledger_balances_check_non_negative();

-- Immutability: journals and entries are append-only.
CREATE FUNCTION ledger_block_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is insert-only and cannot be updated or deleted', TG_TABLE_NAME
        USING ERRCODE = 'LD004';
END;
$$;

CREATE FUNCTION ledger_block_truncate()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is insert-only and cannot be truncated', TG_TABLE_NAME
        USING ERRCODE = 'LD004';
END;
$$;

CREATE TRIGGER trg_ledger_journals_no_update
    BEFORE UPDATE OR DELETE ON ledger_journals
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();

CREATE TRIGGER trg_ledger_journals_no_truncate
    BEFORE TRUNCATE ON ledger_journals
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_truncate();

CREATE TRIGGER trg_ledger_entries_no_update
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();

CREATE TRIGGER trg_ledger_entries_no_truncate
    BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_truncate();

-- The balance projection may be updated (that is its purpose) but never
-- deleted from or truncated.
CREATE TRIGGER trg_ledger_balances_no_delete
    BEFORE DELETE ON ledger_balances
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();

CREATE TRIGGER trg_ledger_balances_no_truncate
    BEFORE TRUNCATE ON ledger_balances
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_truncate();

-- Accounts are never deleted, and their identity columns never change.
CREATE FUNCTION ledger_block_account_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ledger_accounts rows cannot be deleted'
        USING ERRCODE = 'LD004';
END;
$$;

CREATE FUNCTION ledger_block_account_key_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.owner_ref IS DISTINCT FROM OLD.owner_ref
       OR NEW.currency IS DISTINCT FROM OLD.currency THEN
        RAISE EXCEPTION 'ledger account kind, owner and currency are immutable'
            USING ERRCODE = 'LD004';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_ledger_accounts_no_delete
    BEFORE DELETE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_block_account_delete();

CREATE TRIGGER trg_ledger_accounts_no_key_change
    BEFORE UPDATE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_block_account_key_change();

-- Account kinds are a fixed reference table. Guard them against edits, deletes
-- and truncation so a kind's normal side or allow_negative flag cannot drift
-- under existing balances.
CREATE TRIGGER trg_ledger_account_kinds_no_update
    BEFORE UPDATE OR DELETE ON ledger_account_kinds
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();

CREATE TRIGGER trg_ledger_account_kinds_no_truncate
    BEFORE TRUNCATE ON ledger_account_kinds
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_truncate();

-- An entry may only be written in the transaction that created its journal.
-- Read the journal's tx_id and compare with the current transaction id. Both
-- a newly inserted journal in this transaction and a journal committed in a
-- later transaction are rejected here, so entries are always posted with their
-- journal header.
CREATE FUNCTION ledger_check_entry_transaction()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_journal_txid BIGINT;
BEGIN
    SELECT tx_id INTO v_journal_txid
    FROM ledger_journals
    WHERE id = NEW.journal_id;

    IF NOT FOUND THEN
        -- The journal does not exist. Report it as a foreign key violation so
        -- callers see the same class of error as a real FK breach.
        RAISE EXCEPTION 'entry references missing journal %', NEW.journal_id
            USING ERRCODE = '23503';
    END IF;

    IF v_journal_txid IS DISTINCT FROM txid_current() THEN
        RAISE EXCEPTION 'entry for journal % must be written in the journal''s own transaction', NEW.journal_id
            USING ERRCODE = 'LD005';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_ledger_entries_same_transaction
    BEFORE INSERT ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_check_entry_transaction();

-- Balance at commit, checked once per journal.
--
-- A DEFERRABLE INITIALLY DEFERRED constraint trigger on ledger_journals fires
-- at COMMIT, after all rows of the transaction are visible to the check. It
-- verifies, for the journal that was inserted:
--   * at least two legs (LD001),
--   * each entry currency matches its account currency (LD002),
--   * debits equal credits within each currency (LD001).
--
-- Attaching the check to the journal rather than to each entry means it runs
-- once per journal, including for a journal committed with zero entries, which
-- the per-row entry trigger could never see.
CREATE FUNCTION ledger_check_journal_balance()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_legs          INTEGER;
    v_bad_currency  INTEGER;
    v_unbalanced    INTEGER;
BEGIN
    SELECT count(*) INTO v_legs
    FROM ledger_entries WHERE journal_id = NEW.id;

    IF v_legs < 2 THEN
        RAISE EXCEPTION 'journal % has fewer than two entries', NEW.id
            USING ERRCODE = 'LD001';
    END IF;

    SELECT count(*) INTO v_bad_currency
    FROM ledger_entries e
    JOIN ledger_accounts a ON a.id = e.account_id
    WHERE e.journal_id = NEW.id AND e.currency <> a.currency;

    IF v_bad_currency > 0 THEN
        RAISE EXCEPTION 'journal % has entries whose currency differs from their account', NEW.id
            USING ERRCODE = 'LD002';
    END IF;

    SELECT count(*) INTO v_unbalanced
    FROM (
        SELECT currency,
               SUM(CASE WHEN side = 'Dr' THEN amount ELSE 0 END) AS dr,
               SUM(CASE WHEN side = 'Cr' THEN amount ELSE 0 END) AS cr
        FROM ledger_entries
        WHERE journal_id = NEW.id
        GROUP BY currency
    ) per_currency
    WHERE dr <> cr;

    IF v_unbalanced > 0 THEN
        RAISE EXCEPTION 'journal % does not balance per currency', NEW.id
            USING ERRCODE = 'LD001';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_ledger_journals_balance
    AFTER INSERT ON ledger_journals
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_journal_balance();
