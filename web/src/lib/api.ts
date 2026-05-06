import { getCsrfToken } from './utils'
import type {
  DashboardSnapshot,
  MetricsSnapshot,
  JournalEntry,
  JournalAPIResponse,
  ProjectResponse,
  Task,
  TaskDetail,
  TaskDetailAPIResponse,
  SkillInfo,
  SkillRegistryEntry,
  InstalledSkillsResponse,
  SkillRegistryResponse,
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
    // Do not expose response body: it may contain server-internal details.
    throw new Error(`API error ${res.status}`)
  }
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

export async function getProject(): Promise<ProjectResponse> {
  if (USE_MOCK) return (await import('./mock')).mockProject
  return apiFetch<ProjectResponse>('/api/project')
}

export async function getTasks(): Promise<TaskListResponse> {
  if (USE_MOCK) {
    const { mockTasks } = await import('./mock')
    return { tasks: mockTasks, total: mockTasks.length }
  }
  return apiFetch<TaskListResponse>('/api/tasks')
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

export async function getInstalledSkills(): Promise<SkillInfo[]> {
  if (USE_MOCK) return (await import('./mock')).mockSkillsInstalled
  const resp = await apiFetch<InstalledSkillsResponse>('/api/skills/installed')
  return resp.skills
}

export async function searchSkillsRegistry(q: string): Promise<SkillRegistryEntry[]> {
  if (USE_MOCK) {
    const { mockSkillsRegistry } = await import('./mock')
    return mockSkillsRegistry.filter(
      (s) => !q || s.name.includes(q) || s.description.includes(q),
    )
  }
  const resp = await apiFetch<SkillRegistryResponse>(`/api/skills/registry?q=${encodeURIComponent(q)}`)
  return resp.skills
}

export async function addTask(data: {
  source: string
  title: string
  body: string
  priority: number
}): Promise<Task> {
  return apiFetch<Task>('/api/tasks', {
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

export async function sendToWorkspace(id: number, text: string): Promise<void> {
  await apiFetch(`/api/tasks/${id}/send`, {
    method: 'POST',
    headers: mutationHeaders(),
    body: JSON.stringify({ text }),
  })
}
