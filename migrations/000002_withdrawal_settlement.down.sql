-- Reverse 000002_withdrawal_settlement.up.sql.

DROP INDEX IF EXISTS ledger_journals_settles_uniq;

ALTER TABLE ledger_journals
    DROP CONSTRAINT IF EXISTS ledger_journals_settlement_pairing_check;

ALTER TABLE ledger_journals
    DROP COLUMN IF EXISTS settles_journal_id;
