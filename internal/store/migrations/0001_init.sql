-- 0001_init.sql introduces the two tables marunage's queue layer treats as
-- the source of truth (docs/requirement.md "データモデル"):
--
--   tasks    — the persistent task queue. (source, external_id) is unique
--              among non-NULL external_id values so Discovery plugins can
--              re-run idempotently (invariant #4 "Idempotency"), while
--              manually added rows (external_id IS NULL) are not constrained.
--   kv_state — per-source checkpoints (e.g. gmail_last_id, slack_last_ts).
--
-- Column types follow the spec table verbatim. Timestamps are stored as
-- ISO8601 TEXT to keep the file portable and to round-trip cleanly through
-- `sqlite3` for ad-hoc inspection.

CREATE TABLE tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source          TEXT NOT NULL,
    external_id     TEXT,
    external_url    TEXT,
    title           TEXT NOT NULL,
    body            TEXT,
    notes           TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    judgment_reason TEXT,
    priority        INTEGER NOT NULL DEFAULT 0,
    lock_key        TEXT,
    cwd             TEXT,
    ws              TEXT,
    result_summary  TEXT,
    reflection      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    started_at      TEXT,
    completed_at    TEXT
);

-- Partial index: NULL external_id is allowed many times per source (manual
-- adds), but a non-NULL pair must be unique so re-running Discovery cannot
-- duplicate a row.
CREATE UNIQUE INDEX idx_tasks_source_external_id
    ON tasks(source, external_id)
    WHERE external_id IS NOT NULL;

CREATE INDEX idx_tasks_status   ON tasks(status);
CREATE INDEX idx_tasks_priority ON tasks(priority);

CREATE TABLE kv_state (
    key        TEXT PRIMARY KEY,
    value      TEXT,
    updated_at TEXT NOT NULL
);
