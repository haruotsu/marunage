# marunage Web API Reference

## Overview

`marunage web` exposes a local HTTP server with both HTML pages and JSON APIs. All endpoints are served from the same origin; the base URL is configurable at startup (default: `http://localhost:8080`).

### Authentication & CSRF

All mutating requests (POST, PATCH, DELETE) require a valid CSRF token using the **double-submit cookie** pattern.

| Item | Value |
|------|-------|
| Cookie name | `marunage_csrf` |
| Request header | `X-CSRF-Token` |
| Form field (alternative) | `_csrf` |

**How to obtain a token:**

1. Make any GET request (e.g. `GET /`). The response sets a `marunage_csrf` cookie.
2. Echo the cookie value back in the `X-CSRF-Token` header on every mutating request.

**Failure responses:**

| Condition | Status | Body |
|-----------|--------|------|
| Cookie absent | 403 | `csrf: missing cookie` |
| Token absent | 403 | `csrf: missing token` |
| Token mismatch | 403 | `csrf: token mismatch` |

### Common Response Headers

Every response includes baseline security headers:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Content-Security-Policy: default-src 'self'; ...
```

### Conditional Endpoint Availability

Some endpoint groups are only registered when the corresponding provider is wired at startup:

| Endpoint group | Condition |
|----------------|-----------|
| `GET /review`, `GET /api/review/skipped` | `ReviewProvider` wired |
| `POST /api/tasks/{id}/dispatch` and other task mutation endpoints | `TaskOpsStore` wired |

---

## System Endpoints

### GET /healthz

Always returns 200. Use as a liveness probe.

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8

ok
```

---

### GET /events

Global Server-Sent Events feed. Streams hub events (task state changes, discovery events) to connected clients. Emits a `ping` heartbeat every 30 seconds so intermediaries don't drop the idle connection.

**Response headers**

```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

**Event types**

| Event name | Data | Description |
|------------|------|-------------|
| `ping` | Unix timestamp (ms) | Periodic heartbeat; first ping is sent immediately on connect |
| *(named)* | string | Application events published via the internal hub |

**Error responses**

| Status | Condition |
|--------|-----------|
| 503 | Hub subscriber capacity (64) reached; `Retry-After: 30` header included |
| 500 | Server does not support streaming |

**Example stream**

```
event: ping
data: 1714000000000

event: ping
data: 1714000030000
```

---

## Dashboard Endpoints

### GET /

Renders the full dashboard HTML page. Also sets the initial `marunage_csrf` cookie if one is not already present.

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
```

**Error responses**

| Status | Condition |
|--------|-----------|
| 500 | CSRF token generation failed |
| 500 | Template render failed |

---

### GET /partials/dashboard

Returns the dashboard HTML fragment only. Intended for periodic polling by the frontend to refresh the dashboard without a full page reload.

**Response headers**

```
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
```

**Error responses**

| Status | Body | Condition |
|--------|------|-----------|
| 500 | `Dashboard data unavailable. See daemon.log for details.` | Provider error |
| 500 | `render failed` | Template render failed |

---

## Task Endpoints

### GET /tasks/{id}

Renders the task detail HTML page for the task with the given ID.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
```

**Error responses**

| Status | Body | Condition |
|--------|------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 404 | `task not found` | No task with this ID exists |
| 500 | `Task detail unavailable. See daemon.log for details.` | Provider error |

---

### POST /api/tasks

Creates a new manual task. Requires CSRF token.

**Request**

```
POST /api/tasks
Content-Type: application/json
X-CSRF-Token: <token>

{
  "title": "string (required)",
  "body": "string (optional)",
  "priority": 0
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `title` | string | Yes | Task title; must be non-empty after trimming whitespace |
| `body` | string | No | Task body / description |
| `priority` | integer | No | Task priority; defaults to 0 |

**Response — 201 Created**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid JSON body` | Malformed JSON |
| 400 | `title is required` | `title` is blank |
| 500 | `internal error` | Store error |

---

### POST /api/tasks/{id}/dispatch

Transitions a task from `pending` → `running` and stamps `started_at`. Requires CSRF token.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
POST /api/tasks/{id}/dispatch
X-CSRF-Token: <token>
```

No request body required.

**Response — 200 OK**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 404 | `not found` | Task does not exist |
| 409 | `invalid status transition` | Task is not in `pending` state |
| 500 | `internal error` | Store error |

---

### POST /api/tasks/{id}/promote

Transitions a task from `skipped` → `pending`. Requires CSRF token.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
POST /api/tasks/{id}/promote
X-CSRF-Token: <token>
```

No request body required.

**Response — 200 OK**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 404 | `not found` | Task does not exist |
| 409 | `invalid status transition` | Task is not in `skipped` state |
| 500 | `internal error` | Store error |

---

### POST /api/tasks/{id}/reopen

Transitions a task from `done` or `failed` → `pending`. Requires CSRF token.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
POST /api/tasks/{id}/reopen
X-CSRF-Token: <token>
```

No request body required.

**Response — 200 OK**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 404 | `not found` | Task does not exist |
| 409 | `invalid status transition` | Task is not in `done` or `failed` state |
| 500 | `internal error` | Store error |

---

### PATCH /api/tasks/{id}/priority

Updates the priority of an existing task. Requires CSRF token.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
PATCH /api/tasks/{id}/priority
Content-Type: application/json
X-CSRF-Token: <token>

{
  "priority": 10
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `priority` | integer | Yes | New priority value |

**Response — 200 OK**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 400 | `invalid JSON body` | Malformed JSON |
| 404 | `not found` | Task does not exist |
| 500 | `internal error` | Store error |

---

### DELETE /api/tasks/{id}

Deletes a task regardless of its current status. Requires CSRF token.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
DELETE /api/tasks/{id}
X-CSRF-Token: <token>
```

No request body required.

**Response — 200 OK**

```json
{
  "status": "ok",
  "id": 42
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 404 | `not found` | Task does not exist |
| 500 | `internal error` | Store error |

---

## Skills Endpoints

### GET /skills

Renders the skills HTML page listing all registry-installed skills.

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
```

Also sets `marunage_csrf` cookie if not present.

**Error responses**

| Status | Condition |
|--------|-----------|
| 500 | CSRF token generation failed or skills load failed |

---

### GET /api/skills/installed

Returns a JSON list of skills installed under the configured `SkillsRoot` directory.

**Response — 200 OK**

```json
{
  "skills": [
    {
      "name": "review-fix-loop",
      "version": "1.0.0",
      "description": "...",
      "installed_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

When `SkillsRoot` is not configured, `skills` is an empty array.

**Error responses**

| Status | Condition |
|--------|-----------|
| 500 | Failed to read the skills state file |

---

### GET /api/skills/registry

Proxies the upstream skill registry catalog. Supports optional keyword search via `?q=`.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `q` | string | Keyword search filter (optional) |

**Response — 200 OK**

```json
{
  "skills": [
    {
      "name": "simplify",
      "version": "1.2.0",
      "description": "...",
      "author": "..."
    }
  ]
}
```

**Error responses**

| Status | Condition |
|--------|-----------|
| 503 | `RegistryURL` not configured |
| 502 | Upstream registry fetch failed |

---

## Review Endpoints

> These endpoints are only available when a `ReviewProvider` is wired at startup.

### GET /review

Renders the review HTML page listing skipped tasks with a reason frequency report.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `since` | string | Time window filter, e.g. `7d`, `24h` (optional) |

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
```

**Error responses**

| Status | Condition |
|--------|-----------|
| 500 | `Review data unavailable. See daemon.log for details.` |

---

### GET /api/review/skipped

Returns a JSON array of tasks with `skipped` status.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `since` | string | Time window filter, e.g. `7d`, `30d`, `24h` (optional) |

The `since` parameter supports:
- `Nd` — N days (e.g. `7d`, `30d`); max 36500 days
- Any duration accepted by Go's `time.ParseDuration` (e.g. `24h`, `168h`)

**Response — 200 OK**

```json
[
  {
    "id": 42,
    "source": "github",
    "title": "Fix login bug",
    "judgment_reason": "duplicate",
    "status": "skipped",
    "created_at": "2024-01-15T10:30:00Z"
  }
]
```

| Field | Type | Notes |
|-------|------|-------|
| `id` | integer | Task ID |
| `source` | string | Origin of the task (e.g. `github`, `manual`) |
| `title` | string | Task title |
| `judgment_reason` | string | Reason for skipping (omitted if empty) |
| `status` | string | Always `skipped` |
| `created_at` | string | ISO 8601 UTC timestamp (omitted if zero) |

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 500 | `review data unavailable` | Provider error |

---

## Metrics Endpoints

### GET /metrics

Renders the metrics HTML dashboard. When the `Accept` header explicitly includes `text/plain` (and not via `*/*`), returns Prometheus text format instead.

**Query parameters**

None.

**Response (HTML)**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
```

**Response (Prometheus, when `Accept: text/plain`)**

```
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4; charset=utf-8
Cache-Control: no-store
```

---

### GET /api/metrics

Returns a JSON snapshot of aggregated task metrics.

**Response — 200 OK**

```json
{
  "total_tasks": 120,
  "by_status": {
    "done": 95,
    "failed": 10,
    "pending": 5,
    "running": 2,
    "skipped": 8
  },
  "by_source": {
    "github": 80,
    "manual": 40
  },
  "success_rate": 0.857,
  "avg_duration_seconds": 320.5,
  "daily_counts": [
    {
      "date": "2024-01-15",
      "done": 12,
      "failed": 1
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `total_tasks` | integer | Total task count in the snapshot |
| `by_status` | object | Count per status string |
| `by_source` | object | Count per source string |
| `success_rate` | float | Ratio of successfully completed tasks (0–1) |
| `avg_duration_seconds` | float | Average task duration in seconds |
| `daily_counts` | array | Per-day completion counts |
| `daily_counts[].date` | string | `YYYY-MM-DD` |
| `daily_counts[].done` | integer | Tasks completed successfully on that day |
| `daily_counts[].failed` | integer | Tasks that failed on that day |

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 500 | `metrics data unavailable` | Provider error |

---

### GET /prometheus

Returns task metrics in Prometheus exposition format (text format version 0.0.4).

**Response — 200 OK**

```
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4; charset=utf-8
Cache-Control: no-store

# HELP marunage_tasks Total number of tasks (current snapshot)
# TYPE marunage_tasks gauge
marunage_tasks 120

# HELP marunage_tasks_by_status Number of tasks by status
# TYPE marunage_tasks_by_status gauge
marunage_tasks_by_status{status="done"} 95
marunage_tasks_by_status{status="failed"} 10

# HELP marunage_tasks_by_source Number of tasks by source
# TYPE marunage_tasks_by_source gauge
marunage_tasks_by_source{source="github"} 80

# HELP marunage_task_success_ratio Ratio of tasks completed successfully (0–1)
# TYPE marunage_task_success_ratio gauge
marunage_task_success_ratio 0.857

# HELP marunage_task_avg_duration_seconds Average task duration in seconds
# TYPE marunage_task_avg_duration_seconds gauge
marunage_task_avg_duration_seconds 320.5
```

**Exposed metrics**

| Metric | Type | Description |
|--------|------|-------------|
| `marunage_tasks` | gauge | Total task count |
| `marunage_tasks_by_status{status="…"}` | gauge | Task count per status |
| `marunage_tasks_by_source{source="…"}` | gauge | Task count per source |
| `marunage_task_success_ratio` | gauge | Success ratio (0–1) |
| `marunage_task_avg_duration_seconds` | gauge | Average task duration |

**Error responses**

| Status | Condition |
|--------|-----------|
| 500 | `metrics data unavailable` |

> **Tip:** `GET /metrics` with `Accept: text/plain` also returns Prometheus format. Prometheus scrapers can point at either endpoint.

---

## Journal Endpoints

### GET /journal

Renders the work journal HTML page for a given date.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `date` | string | `YYYY-MM-DD` filter (optional; defaults to today) |

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
```

**Error responses**

| Status | Condition |
|--------|-----------|
| 400 | `invalid date: use YYYY-MM-DD format` |
| 500 | `Journal data unavailable. See daemon.log for details.` |

---

### GET /api/journal

Returns journal entries for a given date as JSON.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `date` | string | `YYYY-MM-DD` filter (optional; defaults to today) |

**Response — 200 OK**

```json
{
  "entries": [
    {
      "time": "10:30",
      "source": "github",
      "summary": "Merged PR #42: fix login bug"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `entries` | array | Journal entries for the requested date |
| `entries[].time` | string | Time of the entry |
| `entries[].source` | string | Origin source |
| `entries[].summary` | string | Human-readable activity summary |

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid date: use YYYY-MM-DD format` | Malformed `date` parameter |
| 500 | `journal data unavailable` | Provider error |

---

## Project Endpoints

### GET /project

Renders the GitHub project board HTML page.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `board_url` | string | GitHub Projects URL (optional; defaults to the configured board) |

Only `http` and `https` schemes are accepted for `board_url`.

**Response**

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
```

**Error responses**

| Status | Condition |
|--------|-----------|
| 400 | `invalid board_url: only http and https schemes are allowed` |
| 500 | `Project data unavailable. See daemon.log for details.` |

---

### GET /api/project

Returns GitHub project board state as JSON.

**Query parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `board_url` | string | GitHub Projects URL (optional; defaults to the configured board) |

Only `http` and `https` schemes are accepted for `board_url`.

**Response — 200 OK**

```json
{
  "phases": [
    {
      "name": "In Progress",
      "status": "active",
      "items": [
        {
          "id": "PVT_123",
          "title": "Implement SSE streaming",
          "status": "In Progress"
        }
      ]
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `phases` | array | Project board columns / phases |
| `phases[].name` | string | Phase name |
| `phases[].status` | string | Phase status |
| `phases[].items` | array | Items in this phase |
| `phases[].items[].id` | string | Project item ID |
| `phases[].items[].title` | string | Item title |
| `phases[].items[].status` | string | Item status |

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid board_url: only http and https schemes are allowed` | Invalid URL scheme |
| 500 | `project data unavailable` | Provider error |

---

## Live Stream Endpoints

These endpoints connect the web UI to the cmux terminal workspace associated with a running task.

### GET /api/tasks/{id}/stream

Streams live terminal output from the task's cmux workspace as Server-Sent Events. Polls the workspace every 1 second and emits an `output` event whenever the content changes. Sends a `ping` every 30 seconds to keep the connection alive.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Response headers**

```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

**Event types**

| Event name | Data | Description |
|------------|------|-------------|
| `ping` | `"connected"` (on connect) or Unix timestamp ms (periodic) | Keep-alive heartbeat every 30 seconds |
| `output` | JSON-encoded string of terminal text | Emitted when workspace output changes |

**Example stream**

```
event: ping
data: connected

event: output
data: "$ go test ./...\nok  github.com/haruotsu/marunage\n"

event: ping
data: 1714000030000
```

**Error responses**

| Status | Body | Condition |
|--------|------|-----------|
| 400 | `{"error":"invalid task id"}` | `id` is not a valid integer |
| 404 | `workspace not found` | Task has no associated workspace |
| 500 | `streaming unsupported` | Server does not support `http.Flusher` |
| 500 | `internal error` | Provider error |

---

### POST /api/tasks/{id}/send

Sends text input to the task's cmux workspace. Requires CSRF token. Request body is limited to 64 KiB.

**Path parameters**

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | integer | Task ID |

**Request**

```
POST /api/tasks/{id}/send
Content-Type: application/json
X-CSRF-Token: <token>

{
  "text": "go test ./...\n"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `text` | string | Yes | Text to forward to the workspace terminal; must be non-empty after trimming whitespace |

**Response — 200 OK**

```json
{
  "status": "ok"
}
```

**Error responses**

| Status | `error` | Condition |
|--------|---------|-----------|
| 400 | `invalid task id` | `id` is not a valid integer |
| 400 | `invalid JSON body` | Malformed JSON or body exceeds 64 KiB |
| 400 | `text is required` | `text` field is blank |
| 404 | `workspace not found` | Task has no associated workspace |
| 500 | `send failed` | Workspace send error |
| 500 | `internal error` | Provider error |
