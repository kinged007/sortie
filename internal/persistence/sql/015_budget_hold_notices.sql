-- Migration 15: cross-restart dedup for the budget hold tracker notice
--
-- noticed_at lets an operator match a row to the tracker comment. No runtime
-- decision reads it.

CREATE TABLE budget_hold_notices (
    issue_id   TEXT PRIMARY KEY,
    reason     TEXT NOT NULL,
    noticed_at TEXT NOT NULL
);
