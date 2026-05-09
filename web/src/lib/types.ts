export type TaskStatus =
  | 'pending'
  | 'running'
  | 'done'
  | 'failed'
  | 'skipped'
  | 'waiting_human'

export interface Task {
  id: number
  source: string
  external_id: string
  external_url: string
  title: string
  body: string
  notes: string
  status: TaskStatus
  judgment_reason: string
  priority: number
  lock_key?: string
  cwd?: string
  ws: string
  result_summary: string
  reflection: string
  created_at: string
  updated_at: string
  started_at: string | null
  completed_at: string | null
}

// AuditEntry mirrors the Go web.AuditEntry JSON shape (audit_reader.go).
export interface AuditEntry {
  time: string
  action: string
  task_id: number
  value: string
}

export interface TaskDetail extends Task {
  audit_entries: AuditEntry[]
}

// TaskDetailAPIResponse mirrors the Go taskDetailAPIResponse JSON shape.
// The Go handler wraps the task and audit_entries in a top-level object.
export interface TaskDetailAPIResponse {
  task: Task
  audit_entries: AuditEntry[]
}

export interface RunningTask {
  id: number
  source: string
  title: string
  ws: string
  started_at: string | null
  output_preview: string
}

export interface PendingTask {
  id: number
  source: string
  title: string
  priority: number
  created_at: string
}

export interface SourceStatus {
  name: string
  auth_status: 'authenticated' | 'expired' | 'revoked' | 'not_configured'
  last_listed_at: string | null
}

export interface DashboardSnapshot {
  generated_at: string
  running: RunningTask[]
  pending: PendingTask[]
  pending_count: number
  recent_24h: {
    done_count: number
    failed_count: number
    skipped_count: number
  }
  sources: SourceStatus[]
}

export interface MetricsDailyCount {
  date: string
  done: number
  failed: number
}

export interface MetricsSnapshot {
  total_tasks: number
  by_status: Record<string, number>
  by_source: Record<string, number>
  success_rate: number
  avg_duration_seconds: number
  daily_counts: MetricsDailyCount[]
}

export interface JournalEntry {
  time: string
  source: string
  summary: string
}

// JournalAPIResponse mirrors the Go journalAPIResponse JSON shape.
export interface JournalAPIResponse {
  entries: JournalEntry[]
}

// TaskListResponse mirrors the Go taskListAPIResponse JSON shape.
export interface TaskListResponse {
  tasks: Task[]
  total: number
}
