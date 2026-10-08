-- Interruption timeout policy: optional expires_at (RFC3339 UTC) and the
-- default action the daemon applies when the interruption lapses.
-- default_action semantics:
--   pause    -> close as expired, worker resumes later (worker approval gates)
--   deny     -> close as expired and treat the request as denied
--   escalate -> never silently expire: bump priority to urgent and persist
ALTER TABLE interruptions ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE interruptions ADD COLUMN default_action TEXT NOT NULL DEFAULT 'pause'
    CHECK (default_action IN ('pause','deny','escalate'));
