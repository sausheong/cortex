-- Background "remember" jobs carry the raw text they must process, so a queued
-- job survives a restart. attempts and run_after let a job that failed for a
-- transient reason (provider outage) be retried later with a backoff.
ALTER TABLE jobs
	ADD COLUMN payload   TEXT        NOT NULL DEFAULT '',
	ADD COLUMN attempts  INT         NOT NULL DEFAULT 0,
	ADD COLUMN run_after TIMESTAMPTZ NOT NULL DEFAULT now();
