-- Reverse 000001_ledger_core.up.sql completely.
--
-- Drop in dependency order: triggers and functions first, then tables that
-- reference others, then standalone tables. Everything created by the up
-- migration is removed, leaving no ledger objects behind.

-- Constraint trigger on journals.
DROP TRIGGER IF EXISTS trg_ledger_journals_balance ON ledger_journals;

-- Entry guards.
DROP TRIGGER IF EXISTS trg_ledger_entries_same_transaction ON ledger_entries;
DROP TRIGGER IF EXISTS trg_ledger_entries_no_truncate ON ledger_entries;
DROP TRIGGER IF EXISTS trg_ledger_entries_no_update ON ledger_entries;

-- Journal guards.
DROP TRIGGER IF EXISTS trg_ledger_journals_no_truncate ON ledger_journals;
DROP TRIGGER IF EXISTS trg_ledger_journals_no_update ON ledger_journals;

-- Balance guards (delete and truncate only; update is allowed).
DROP TRIGGER IF EXISTS trg_ledger_balances_no_truncate ON ledger_balances;
DROP TRIGGER IF EXISTS trg_ledger_balances_no_delete ON ledger_balances;
DROP TRIGGER IF EXISTS trg_ledger_balances_non_negative ON ledger_balances;

-- Account guards.
DROP TRIGGER IF EXISTS trg_ledger_account_kinds_no_truncate ON ledger_account_kinds;
DROP TRIGGER IF EXISTS trg_ledger_account_kinds_no_update ON ledger_account_kinds;
DROP TRIGGER IF EXISTS trg_ledger_accounts_no_key_change ON ledger_accounts;
DROP TRIGGER IF EXISTS trg_ledger_accounts_no_delete ON ledger_accounts;

-- Trigger functions.
DROP FUNCTION IF EXISTS ledger_check_journal_balance();
DROP FUNCTION IF EXISTS ledger_check_entry_transaction();
DROP FUNCTION IF EXISTS ledger_block_account_key_change();
DROP FUNCTION IF EXISTS ledger_block_account_delete();
DROP FUNCTION IF EXISTS ledger_block_truncate();
DROP FUNCTION IF EXISTS ledger_block_mutation();
DROP FUNCTION IF EXISTS ledger_balances_check_non_negative();

-- Tables, children before parents.
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_balances;
DROP TABLE IF EXISTS ledger_journals;
DROP TABLE IF EXISTS ledger_accounts;
DROP TABLE IF EXISTS ledger_account_kinds;
