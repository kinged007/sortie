-- Migration 38: reaction activity per stage pass
--
-- reaction_activity holds what each pull request reaction did in one pass of
-- an issue, a pass being the runs that share a chain_id. A row is appended for
-- a watch, a scheduled continuation, an action, or an escalation, and nothing
-- updates, deletes, or prunes a row. The unique index keeps one watch row per
-- issue, pass, and kind. Passes that began before this migration are not
-- backfilled, and the reader treats them as unrecorded.

CREATE TABLE reaction_activity (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    TEXT    NOT NULL,
    chain_id    TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    activity    TEXT    NOT NULL,
    recorded_at TEXT    NOT NULL
);

CREATE INDEX idx_reaction_activity_pass ON reaction_activity(issue_id, chain_id);

CREATE UNIQUE INDEX idx_reaction_activity_watch
    ON reaction_activity(issue_id, chain_id, kind) WHERE activity = 'watch';
