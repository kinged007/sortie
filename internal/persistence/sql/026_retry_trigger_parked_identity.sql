-- Migration 26: retry trigger and parked issue identity

ALTER TABLE retry_entries ADD COLUMN trigger TEXT NOT NULL DEFAULT '';
ALTER TABLE parked_issues ADD COLUMN issue_title TEXT NOT NULL DEFAULT '';
ALTER TABLE parked_issues ADD COLUMN issue_url TEXT NOT NULL DEFAULT '';
