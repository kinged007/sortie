-- Migration 34: attention acknowledgements
--
-- One row per acknowledged attention item, keyed by the item's id. The id
-- carries the occurrence of its cause, so a row never matches a new cause. The
-- poll tick deletes the row of every item it no longer derives.

CREATE TABLE attention_acknowledgements (
    item_id         TEXT PRIMARY KEY,
    acknowledged_at TEXT NOT NULL
);
