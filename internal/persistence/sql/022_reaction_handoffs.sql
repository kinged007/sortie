-- Migration 22: review comments each issue's runs were given
--
-- Rows are never deleted, so a comment the agent already received is never
-- handed over as new again, even after a restart.

CREATE TABLE reaction_handoffs (
    issue_id   TEXT NOT NULL,
    kind       TEXT NOT NULL,
    comment_id TEXT NOT NULL,
    PRIMARY KEY (issue_id, kind, comment_id)
);
