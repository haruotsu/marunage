import { getCsrfToken, setCsrfToken } from './utils'
import type {
  DashboardSnapshot,
  MetricsSnapshot,
  JournalEntry,
  JournalAPIResponse,
  Task,
  TaskDetail,
  TaskDetailAPIResponse,
  TaskListResponse,
} from './types'

const USE_MOCK = process.env.NEXT_PUBLIC_USE_MOCK === 'true'

async function apiFetch<T>(
  path: string,
  options?: RequestInit,
): Promise<T> {
  const res = await fetch(path, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  })
  if (!res.ok) {
    let message = `API error ${res.status}`
    try {
      const body = await res.json() as { error?: string }
      if (body.error) message = body.error
    } catch {
      // Non-JSON body: fall through to generic message
    }
    throw new Error(message)
  }
  const csrfToken = res.headers.get('X-CSRF-Token')
  if (csrfToken) setCsrfToken(csrfToken)
  return res.json() as Promise<T>
}

function mutationHeaders(): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    'X-CSRF-Token': getCsrfToken(),
  }
}

export async function getDashboard(): Promise<DashboardSnapshot> {
  if (USE_MOCK) return (await import('./mock')).mockDashboard
  return apiFetch<DashboardSnapshot>('/api/dashboard')
}

export async function getMetrics(): Promise<MetricsSnapshot> {
  if (USE_MOCK) return (await import('./mock')).mockMetrics
  return apiFetch<MetricsSnapshot>('/api/metrics')
}

export async function getJournal(date: string): Promise<JournalEntry[]> {
  if (USE_MOCK) return (await import('./mock')).mockJournalEntries
  const resp = await apiFetch<JournalAPIResponse>(`/api/journal?date=${encodeURIComponent(date)}`)
  return resp.entries
}

export async function getTasks(status?: string): Promise<TaskListResponse> {
  if (USE_MOCK) {
    const { mockTasks } = await import('./mock')
    const tasks = status ? mockTasks.filter((t) => t.status === status) : mockTasks
    return { tasks, total: tasks.length }
  }
  const qs = status ? `?status=${encodeURIComponent(status)}` : ''
  return apiFetch<TaskListResponse>(`/api/tasks${qs}`)
}

export async function getTask(id: number): Promise<TaskDetail> {
  if (USE_MOCK) return (await import('./mock')).mockTaskDetail
  // Go returns { task: {...}, audit_entries: [...] }; flatten into TaskDetail.
  const resp = await apiFetch<TaskDetailAPIResponse>(`/api/tasks/${id}`)
  return { ...resp.task, audit_entries: resp.audit_entries }
}

export async function getSkippedTasks(): Promise<Task[]> {
  if (USE_MOCK) {
    const { mockTasks } = await import('./mock')
    return mockTasks.filter((t) => t.status === 'skipped')
  }
  return apiFetch<Task[]>('/api/review/skipped')
}

export async function addTask(data: {
  source: string
  title: string
  body: string
  cwd?: string
  priority: number
}): Promise<{ status: string; id: number }> {
  return apiFetch<{ status: string; id: number }>('/api/tasks', {
    method: 'POST',
    headers: mutationHeaders(),
    body: JSON.stringify(data),
  })
}

export async function dispatchTask(id: number): Promise<void> {
  await apiFetch(`/api/tasks/${id}/dispatch`, {
    method: 'POST',
    headers: mutationHeaders(),
  })
}

export async function promoteTask(id: number): Promise<void> {
  await apiFetch(`/api/tasks/${id}/promote`, {
    method: 'POST',
    headers: mutationHeaders(),
  })
}

export async function reopenTask(id: number): Promise<void> {
  await apiFetch(`/api/tasks/${id}/reopen`, {
    method: 'POST',
    headers: mutationHeaders(),
  })
}

export async function deleteTask(id: number): Promise<void> {
  await apiFetch(`/api/tasks/${id}`, {
    method: 'DELETE',
    headers: mutationHeaders(),
  })
}

export async function updateTaskPriority(id: number, priority: number): Promise<void> {
  await apiFetch(`/api/tasks/${id}/priority`, {
    method: 'PATCH',
    headers: mutationHeaders(),
    body: JSON.stringify({ priority }),
  })
}
