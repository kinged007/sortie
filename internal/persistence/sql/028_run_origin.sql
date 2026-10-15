-- Migration 28: run origin on run history and retry entries
--
-- The run_history columns stay nullable: NULL means not recorded, which ''
-- would misstate as no retry trigger or a local run.

ALTER TABLE run_history ADD COLUMN trigger TEXT;
ALTER TABLE run_history ADD COLUMN reaction_kind TEXT;
ALTER TABLE run_history ADD COLUMN session_id TEXT;
ALTER TABLE run_history ADD COLUMN ssh_host TEXT;
CREATE INDEX idx_run_history_identifier ON run_history(identifier);
ALTER TABLE retry_entries ADD COLUMN origin_trigger TEXT NOT NULL DEFAULT '';

UPDATE retry_entries SET origin_trigger = trigger WHERE trigger NOT IN ('timer', '');
