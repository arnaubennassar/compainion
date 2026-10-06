-- 001_init.sql: complete initial schema for CompAInion.
-- Conventions: ids are ULIDs (TEXT), times are RFC3339 UTC TEXT, JSON-valued
-- columns (handle, scope, acceptance_checks, evidence, payload, outcome,
-- types, filter) are TEXT holding JSON.
-- NOTE: schema_migrations is created by db.go before applying migrations.

CREATE TABLE workstreams (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('active','paused','done','archived')),
    priority    INTEGER NOT NULL DEFAULT 2,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE agents (
    id                 TEXT PRIMARY KEY,
    role               TEXT NOT NULL CHECK (role IN ('companion','orchestrator','worker')),
    parent_id          TEXT REFERENCES agents(id),
    harness            TEXT NOT NULL DEFAULT '',
    handle             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL CHECK (status IN ('starting','running','waiting','idle','lost','finished','failed')),
    workstream_id      TEXT REFERENCES workstreams(id),
    last_heartbeat_at  TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE plans (
    id                    TEXT PRIMARY KEY,
    workstream_id         TEXT NOT NULL REFERENCES workstreams(id),
    title                 TEXT NOT NULL,
    summary               TEXT NOT NULL DEFAULT '',
    goals                 TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    acceptance_checks     TEXT NOT NULL DEFAULT '[]',
    scope                 TEXT NOT NULL DEFAULT '{}',
    status                TEXT NOT NULL CHECK (status IN ('draft','approved','running','blocked','done','failed','cancelled')),
    creator_agent_id      TEXT REFERENCES agents(id),
    orchestrator_agent_id TEXT REFERENCES agents(id),
    goal_step_id          TEXT,
    approved_by           TEXT,
    approved_at           TEXT,
    version               INTEGER NOT NULL DEFAULT 1,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE TABLE steps (
    id                   TEXT PRIMARY KEY,
    plan_id              TEXT NOT NULL REFERENCES plans(id),
    kind                 TEXT NOT NULL CHECK (kind IN ('task','checkpoint','goal')),
    title                TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    acceptance_criteria  TEXT NOT NULL DEFAULT '',
    scope                TEXT NOT NULL DEFAULT '{}',
    status               TEXT NOT NULL CHECK (status IN ('pending','in_progress','done','failed','blocked','interrupted','cancelled')),
    assignee_agent_id    TEXT REFERENCES agents(id),
    outcome              TEXT,
    attempt              INTEGER NOT NULL DEFAULT 0,
    added_by             TEXT NOT NULL CHECK (added_by IN ('planner','orchestrator','user')),
    suggested_executor   TEXT NOT NULL DEFAULT '',
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE step_deps (
    step_id       TEXT NOT NULL REFERENCES steps(id),
    depends_on_id TEXT NOT NULL REFERENCES steps(id),
    PRIMARY KEY (step_id, depends_on_id)
);

CREATE TABLE tasks (
    id                   TEXT PRIMARY KEY,
    workstream_id        TEXT NOT NULL REFERENCES workstreams(id),
    requested_by         TEXT NOT NULL,
    title                TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    acceptance_criteria  TEXT NOT NULL DEFAULT '',
    scope                TEXT NOT NULL DEFAULT '{}',
    status               TEXT NOT NULL CHECK (status IN ('pending','in_progress','done','failed','blocked','interrupted','cancelled')),
    assignee_agent_id    TEXT REFERENCES agents(id),
    outcome              TEXT,
    attempt              INTEGER NOT NULL DEFAULT 0,
    added_by             TEXT NOT NULL,
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE interruptions (
    id                 TEXT PRIMARY KEY,
    workstream_id      TEXT REFERENCES workstreams(id),
    raised_by_agent_id TEXT REFERENCES agents(id),
    plan_id            TEXT REFERENCES plans(id),
    step_id            TEXT REFERENCES steps(id),
    task_id            TEXT REFERENCES tasks(id),
    topic              TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('approval','question','finding','info')),
    priority           TEXT NOT NULL CHECK (priority IN ('urgent','high','normal','low')),
    digest             TEXT NOT NULL DEFAULT '',
    blocking           INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL CHECK (status IN ('open','presented','answered','dismissed')),
    answered_at        TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

-- "group" is a reserved word in SQL -> column grp; the JSON name stays "group".
CREATE TABLE questions (
    id              TEXT PRIMARY KEY,
    interruption_id TEXT NOT NULL REFERENCES interruptions(id),
    position        INTEGER NOT NULL,
    text            TEXT NOT NULL,
    answer_type     TEXT NOT NULL CHECK (answer_type IN ('choice','multi_choice','free_text','confirm')),
    grp             TEXT,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','answered','skipped')),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE suggestions (
    id          TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id),
    label       TEXT NOT NULL,
    rationale   TEXT NOT NULL DEFAULT '',
    recommended INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX one_recommended ON suggestions(question_id) WHERE recommended = 1;

CREATE TABLE answers (
    id            TEXT PRIMARY KEY,
    question_id   TEXT NOT NULL REFERENCES questions(id),
    author        TEXT NOT NULL CHECK (author IN ('user','companion')),
    mode          TEXT NOT NULL CHECK (mode IN ('suggestion','suggestion_with_comment','free','needs_details','pushback')),
    suggestion_id TEXT REFERENCES suggestions(id),
    text          TEXT,
    final         INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE findings (
    id                  TEXT PRIMARY KEY,
    reported_by_agent_id TEXT REFERENCES agents(id),
    plan_id             TEXT REFERENCES plans(id),
    step_id             TEXT REFERENCES steps(id),
    category            TEXT NOT NULL CHECK (category IN ('bug','security','performance','improvement','question','note')),
    severity            TEXT NOT NULL CHECK (severity IN ('info','low','medium','high','critical')),
    title               TEXT NOT NULL,
    details             TEXT NOT NULL DEFAULT '',
    fingerprint         TEXT NOT NULL,
    occurrences         INTEGER NOT NULL DEFAULT 1,
    evidence            TEXT NOT NULL DEFAULT '[]',
    status              TEXT NOT NULL CHECK (status IN ('new','surfaced','resolved')),
    resolution          TEXT CHECK (resolution IS NULL OR resolution IN ('issue_opened','step_added','ignored')),
    resolution_ref      TEXT,
    interruption_id     TEXT REFERENCES interruptions(id),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX findings_open_fingerprint ON findings(fingerprint)
    WHERE status != 'resolved' OR resolution NOT IN ('issue_opened','ignored');

CREATE TABLE events (
    id        TEXT PRIMARY KEY,
    agent_id  TEXT REFERENCES agents(id),
    plan_id   TEXT REFERENCES plans(id),
    step_id   TEXT REFERENCES steps(id),
    task_id   TEXT REFERENCES tasks(id),
    type      TEXT NOT NULL CHECK (type IN ('started','progress','needs_input','finished','steer','error','note')),
    payload   TEXT NOT NULL DEFAULT '{}',
    at        TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE subscriptions (
    id         TEXT PRIMARY KEY,
    method     TEXT NOT NULL CHECK (method IN ('webhook','command')),
    target     TEXT NOT NULL,
    types      TEXT NOT NULL DEFAULT '[]',
    filter     TEXT NOT NULL DEFAULT '{}',
    secret     TEXT,
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (method, target)
);

CREATE TABLE idempotency_keys (
    key           TEXT NOT NULL,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    request_hash  TEXT NOT NULL,
    status_code   INTEGER NOT NULL,
    response_body TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (key, method, path)
);