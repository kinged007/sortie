-- Migration 32: first run after a stage hop on run history
--
-- Within one issue the lowest id among the rows of a hop is the hop's first
-- run, because one issue runs one run at a time and a row is appended at the
-- run's exit. The rule reads neither trigger nor reaction_kind, so a first
-- run recorded before migration 28 with a NULL trigger still anchors its hop.

ALTER TABLE run_history ADD COLUMN first_after_hop INTEGER NOT NULL DEFAULT 0;

UPDATE run_history SET first_after_hop = 1
WHERE stage_previous <> ''
  AND chain_id <> ''
  AND NOT EXISTS (
    SELECT 1 FROM run_history p
    WHERE p.issue_id = run_history.issue_id
      AND p.chain_id = run_history.chain_id
      AND p.rule_name = run_history.rule_name
      AND p.stage_previous = run_history.stage_previous
      AND p.id < run_history.id);
