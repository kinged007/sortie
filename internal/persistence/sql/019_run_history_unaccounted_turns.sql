-- Migration 19: unaccounted-turn count on run_history

ALTER TABLE run_history ADD COLUMN unaccounted_turns INTEGER NOT NULL DEFAULT 0;
