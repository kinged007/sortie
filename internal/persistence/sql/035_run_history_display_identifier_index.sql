-- Migration 35: display_identifier index on run_history
--
-- Serves the issue key of run history by issue, which matches the qualified
-- form (owner/repo#9) a tracker records beside the bare identifier.

CREATE INDEX idx_run_history_display_identifier ON run_history(display_identifier);
