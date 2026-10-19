-- Migration 36: dispatch ID on run history and retained run events
--
-- run_history.dispatch_id stays nullable and unfilled: NULL marks a row
-- written before the column existed, which '' would misstate as a run that
-- had no dispatch. The index on run_events covers the pruning walk, so it
-- reads no payload. run_events_total.bytes is the sum of run_events.size,
-- kept by every write so pruning never sums the table, and it starts exact
-- because the table starts empty.

ALTER TABLE run_history ADD COLUMN dispatch_id TEXT;

CREATE TABLE run_events (
    dispatch_id  TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    at           TEXT    NOT NULL,
    event        TEXT    NOT NULL,
    completed_at TEXT    NOT NULL,
    size         INTEGER NOT NULL,
    payload      TEXT    NOT NULL,
    PRIMARY KEY (dispatch_id, seq)
);

CREATE INDEX idx_run_events_completed_at ON run_events (completed_at, dispatch_id, size);

CREATE TABLE run_events_total (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    bytes INTEGER NOT NULL
);

INSERT INTO run_events_total (id, bytes) VALUES (1, 0);
