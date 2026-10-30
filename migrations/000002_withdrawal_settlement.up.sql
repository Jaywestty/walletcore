-- Withdrawal settlement linkage.
--
-- A withdrawal is a two-step flow: a withdrawal_request moves funds from the
-- user's wallet into holds, and exactly one settlement journal
-- (withdrawal_finalize or withdrawal_fail) resolves it. The settlement journal
-- records the request it settles in settles_journal_id, and a partial unique
-- index makes settling a request at most once.
--
-- The immutability triggers on ledger_journals and the deferred balance check
-- on ledger_journals already apply to rows in this table and cover the new
-- column, so no new trigger is needed.

ALTER TABLE ledger_journals
    ADD COLUMN settles_journal_id UUID REFERENCES ledger_journals (id);

-- Only a settlement journal carries a target, and every settlement must.
ALTER TABLE ledger_journals
    ADD CONSTRAINT ledger_journals_settlement_pairing_check
    CHECK ((kind IN ('withdrawal_finalize', 'withdrawal_fail')) = (settles_journal_id IS NOT NULL));

-- A request may be settled at most once.
CREATE UNIQUE INDEX ledger_journals_settles_uniq
    ON ledger_journals (settles_journal_id)
    WHERE settles_journal_id IS NOT NULL;
