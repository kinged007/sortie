-- Migration 16: record whether a session's API request count is a measurement
--
-- Defaults to 0, unlike run_history.tokens_measured: an older row's count
-- was written whether or not it was measured.

ALTER TABLE session_metadata ADD COLUMN api_requests_measured INTEGER NOT NULL DEFAULT 0;
