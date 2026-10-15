-- Migration 17: record the dispatch ID that last stored a session_metadata row
--
-- Defaults to '' so an older row never matches a running dispatch.

ALTER TABLE session_metadata ADD COLUMN dispatch_id TEXT NOT NULL DEFAULT '';
