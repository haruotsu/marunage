import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import type { Task, TaskDetail } from '@/lib/types'
import { TaskListView, TaskDetailContent } from './page'

const mockGetTasks = vi.fn()
const mockGetTask = vi.fn()
const mockDeleteTask = vi.fn()
const mockPush = vi.fn()

vi.mock('@/lib/api', () => ({
  getTasks: () => mockGetTasks(),
  getTask: (...args: unknown[]) => mockGetTask(...args),
  dispatchTask: vi.fn(),
  promoteTask: vi.fn(),
  reopenTask: vi.fn(),
  deleteTask: (...args: unknown[]) => mockDeleteTask(...args),
}))

vi.mock('next/navigation', () => ({
  useSearchParams: vi.fn(() => ({ get: (_key: string) => null })),
  useRouter: vi.fn(() => ({ push: mockPush })),
}))

vi.mock('@/components/task-card', () => ({
  TaskCard: ({ task }: { task: Task }) => (
    <div data-testid="task-card">{task.title}</div>
  ),
}))

vi.mock('@/components/status-badge', () => ({
  StatusBadge: () => null,
}))

function makeTask(overrides: Partial<Task> = {}): Task {
  const id = overrides.id ?? 1
  return {
    id,
    source: 'github',
    external_id: `ext-${id}`,
    external_url: `https://example.com/${id}`,
    title: 'Test Task',
    body: '',
    notes: '',
    status: 'pending',
    judgment_reason: '',
    priority: 0,
    ws: 'ws-1',
    result_summary: '',
    reflection: '',
    created_at: '2025-01-01T00:00:00Z',
    updated_at: '2025-01-01T00:00:00Z',
    started_at: null,
    completed_at: null,
    ...overrides,
  }
}

function makeTaskDetail(overrides: Partial<Task> = {}): TaskDetail {
  const base = makeTask(overrides)
  return { ...base, audit_entries: [] }
}

describe('TaskDetailContent delete', () => {
  beforeEach(() => {
    mockGetTask.mockReset()
    mockDeleteTask.mockReset()
    mockPush.mockReset()
  })

  it('navigates to /tasks after successful delete', async () => {
    const { useSearchParams } = await import('next/navigation')
    vi.mocked(useSearchParams).mockReturnValue({ get: (key: string) => key === 'id' ? '1' : null } as ReturnType<typeof useSearchParams>)

    mockGetTask.mockResolvedValue(makeTaskDetail({ id: 1, title: 'To Delete' }))
    mockDeleteTask.mockResolvedValue(undefined)

    render(<TaskDetailContent />)

    await waitFor(() => {
      expect(screen.getByText('Delete')).toBeTruthy()
    })

    fireEvent.click(screen.getByText('Delete'))

    await waitFor(() => {
      expect(mockPush).toHaveBeenCalledWith('/tasks')
    })
  })
})

describe('TaskListView', () => {
  beforeEach(() => {
    mockGetTasks.mockReset()
  })

  it('shows loading spinner while fetching', () => {
    mockGetTasks.mockReturnValue(new Promise(() => {}))
    render(<TaskListView />)
    expect(screen.getByTestId('loading')).toBeTruthy()
  })

  it('renders task cards after successful fetch', async () => {
    const tasks = [makeTask({ id: 1, title: 'Alpha' }), makeTask({ id: 2, title: 'Beta' })]
    mockGetTasks.mockResolvedValue({ tasks, total: 2 })
    render(<TaskListView />)
    await waitFor(() => {
      expect(screen.getAllByTestId('task-card')).toHaveLength(2)
    })
    expect(screen.getByText('Alpha')).toBeTruthy()
    expect(screen.getByText('Beta')).toBeTruthy()
  })

  it('shows total count in header', async () => {
    const tasks = [makeTask({ id: 1, title: 'Only Task' })]
    mockGetTasks.mockResolvedValue({ tasks, total: 1 })
    render(<TaskListView />)
    await waitFor(() => {
      expect(screen.getByText('(1)')).toBeTruthy()
    })
  })

  it('shows empty state when no tasks exist', async () => {
    mockGetTasks.mockResolvedValue({ tasks: [], total: 0 })
    render(<TaskListView />)
    await waitFor(() => {
      expect(screen.getByText('No tasks found.')).toBeTruthy()
    })
  })

  it('shows error message when fetch fails', async () => {
    mockGetTasks.mockRejectedValue(new Error('network error'))
    render(<TaskListView />)
    await waitFor(() => {
      expect(screen.getByText('network error')).toBeTruthy()
    })
  })

  it('shows fallback error for non-Error rejections', async () => {
    mockGetTasks.mockRejectedValue('unexpected')
    render(<TaskListView />)
    await waitFor(() => {
      expect(screen.getByText('Failed to load tasks')).toBeTruthy()
    })
  })
})
