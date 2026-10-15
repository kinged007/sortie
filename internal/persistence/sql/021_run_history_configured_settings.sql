-- Migration 21: configured model, configured effort, and reported model
-- on run_history

ALTER TABLE run_history ADD COLUMN configured_model TEXT NOT NULL DEFAULT '';
ALTER TABLE run_history ADD COLUMN configured_effort TEXT NOT NULL DEFAULT '';
ALTER TABLE run_history ADD COLUMN reported_model TEXT NOT NULL DEFAULT '';
