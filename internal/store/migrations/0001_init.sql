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
-- ISO8601 UTC TEXT in `YYYY-MM-DDTHH:MM:SS.sssZ` (RFC3339 with millisecond
-- precision) so lexicographic ORDER BY matches chronological order and the
-- file round-trips cleanly through `sqlite3` for ad-hoc inspection.
--
-- DB-level invariants enforced here (so a typo in PR-11 / PR-20 cannot
-- silently violate them):
--   - status CHECK pins the documented enum
--   - notes CHECK requires json_valid() so PR-42 dispatch can rely on
--     json_extract(notes, '$.lock_hint') without silent NULL fallbacks
--   - tasks_set_updated_at trigger keeps updated_at honest even when
--     callers forget to set it on UPDATE

CREATE TABLE tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source          TEXT NOT NULL,
    external_id     TEXT,
    external_url    TEXT,
    title           TEXT NOT NULL,
    body            TEXT,
    notes           TEXT CHECK (notes IS NULL OR json_valid(notes)),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','running','done','failed','skipped','waiting_human')),
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

-- Dispatch query (PR-42) is `WHERE status='pending' ORDER BY priority DESC,
-- created_at ASC LIMIT N`. A leading-equality + ordering composite index
-- lets SQLite serve both clauses from one structure (no SCAN, no extra
-- sort). id is appended for stable tie-breaking and to make the index
-- covering for the typical "next dispatch candidate" probe.
CREATE INDEX idx_tasks_dispatch
    ON tasks(status, priority DESC, created_at, id);

-- Soft-lock probe (PR-42) is "any running task with this lock_key?". The
-- partial index avoids carrying NULL rows that the probe would skip anyway.
CREATE INDEX idx_tasks_lock_key
    ON tasks(lock_key)
    WHERE lock_key IS NOT NULL;

-- Auto-bump updated_at on any UPDATE that did not explicitly set it. The
-- WHEN clause lets callers override with a specific timestamp (e.g. tests,
-- import flows) without the trigger fighting them.
CREATE TRIGGER tasks_set_updated_at
AFTER UPDATE ON tasks
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE tasks
       SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE id = NEW.id;
END;

CREATE TABLE kv_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
