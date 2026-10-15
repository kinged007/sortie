-- Migration 33: reaction escalation records
--
-- One row per issue and reaction kind. A standing escalation has a non-NULL
-- escalated_at. Ending an escalation sets escalated_at to NULL and keeps the
-- row, because its subject latches the escalation action to once per subject.
-- The pull request columns let a restart rebuild the pending entry without
-- the workspace.

CREATE TABLE reaction_escalations (
    issue_id           TEXT    NOT NULL,
    kind               TEXT    NOT NULL,
    subject            TEXT    NOT NULL DEFAULT '',
    escalated_at       TEXT,
    escalation_trigger TEXT    NOT NULL,
    attempts           INTEGER NOT NULL DEFAULT 0,
    pr_number          INTEGER NOT NULL,
    pr_owner           TEXT    NOT NULL,
    pr_repo            TEXT    NOT NULL,
    pr_branch          TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (issue_id, kind)
);
