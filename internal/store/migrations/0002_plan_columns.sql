-- 0002_plan_columns.sql adds the management-layer (PR-R03) verdict state to
-- the tasks table. The management layer (internal/manage) evaluates each
-- candidate and records its verdict here so the dispatcher can later restrict
-- itself to rows the manager marked ready (redesign §5.2).
--
-- All columns are added nullable with no DEFAULT, so the ADD COLUMN runs in
-- O(1) and every pre-existing row reads back NULL — the new state is opt-in
-- and existing behaviour is unchanged (strangler fig: redesign §8 PR-R04).
--
-- Deliberately NO CHECK enum on plan_label: status carries a fixed enum
-- because the DB is its source of truth, but the verdict vocabulary is
-- explicitly extensible (redesign D6/D8 原則1 "verdict 語彙と status 語彙を
-- 分離"). A future verdict (e.g. "delegate") must persist without a schema
-- change, so plan_label stays free TEXT and the verdict->status mapping lives
-- in config ([manage.verdicts]).
--
--   plan_label  — management verdict (ready/hold/defer/needs-human/drop/...)
--   plan_reason — rule- or LLM-derived rationale for the verdict
--   plan_score  — LLM scoring value used to order ready rows (nullable)
--   plan_rank   — execution order within ready (nullable)
--   planned_at  — ISO8601 UTC timestamp of the last management evaluation

ALTER TABLE tasks ADD COLUMN plan_label  TEXT;
ALTER TABLE tasks ADD COLUMN plan_reason TEXT;
ALTER TABLE tasks ADD COLUMN plan_score  REAL;
ALTER TABLE tasks ADD COLUMN plan_rank   INTEGER;
ALTER TABLE tasks ADD COLUMN planned_at  TEXT;
