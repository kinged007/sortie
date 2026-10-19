-- Migration 37: blocked runs on run history and live passes
--
-- run_history.agent_blocked is 1 on a run whose agent ended it with a blocked
-- soft stop, which the run records as succeeded with no error, so no other
-- column tells it from a handoff. Rows written before the column existed keep
-- 0 and are not backfilled. live_passes holds the stage passes the
-- orchestrator keeps outside the retry queue, a running session or a budget
-- hold. The orchestrator empties it at startup and restores nothing.

ALTER TABLE run_history ADD COLUMN agent_blocked INTEGER NOT NULL DEFAULT 0;

CREATE TABLE live_passes (
    issue_id       TEXT NOT NULL,
    kind           TEXT NOT NULL,
    chain_id       TEXT NOT NULL,
    rule_name      TEXT NOT NULL,
    stage_previous TEXT NOT NULL,
    PRIMARY KEY (issue_id, kind)
);
