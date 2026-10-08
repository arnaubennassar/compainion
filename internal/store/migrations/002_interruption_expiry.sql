-- Interruption timeout policy: optional expires_at (RFC3339 UTC) recorded as
-- metadata. Expiry can only escalate urgency (bump priority to urgent once +
-- notifying event); it never closes or decides the interruption — user
-- decisions wait until answered. default_action is therefore fixed to
-- 'escalate'.
ALTER TABLE interruptions ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE interruptions ADD COLUMN default_action TEXT NOT NULL DEFAULT 'escalate'
    CHECK (default_action = 'escalate');