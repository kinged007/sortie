-- Migration 20: cache_write_tokens column on run_history,
-- session_metadata, and aggregate_metrics
--
-- Counts input tokens written to the prompt cache, disjoint from the
-- existing cache-read column. Earlier writers folded this count into
-- input and dropped it, so pre-migration rows read zero here and price
-- their cache writes at the input rate, same as before this migration.

ALTER TABLE run_history ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_metadata ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE aggregate_metrics ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
