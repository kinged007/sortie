-- Migration 13: reset points for the consecutive handoff-absence sequence
--
-- A verdict is not stored on run_history: 'succeeded' does not assert that
-- work was observed.

CREATE TABLE handoff_absence_resets (
    issue_id     TEXT    PRIMARY KEY,
    reset_run_id INTEGER NOT NULL,
    updated_at   TEXT    NOT NULL
);
