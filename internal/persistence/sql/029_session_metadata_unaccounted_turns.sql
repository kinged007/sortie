-- Migration 29: unaccounted-turn count on session_metadata

ALTER TABLE session_metadata ADD COLUMN unaccounted_turns INTEGER NOT NULL DEFAULT 0;
