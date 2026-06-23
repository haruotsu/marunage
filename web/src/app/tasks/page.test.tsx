import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import type { Task, TaskDetail } from '@/lib/types'
import { TaskListView, TaskDetailContent } from './page'

const mockGetTasks = vi.fn()
const mockGetTask = vi.fn()
const mockDeleteTask = vi.fn()
const mockPush = vi.fn()

vi.mock('@/lib/api', () => ({
  getTasks: (...args: unknown[]) => mockGetTasks(...args),
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
  TaskCard: ({ task, onDelete }: { task: Task; onDelete?: (id: number) => void }) => (
    <div data-testid="task-card">
      {task.title}
      {onDelete && (
        <button aria-label={`delete-${task.id}`} onClick={() => onDelete(task.id)}>
          del
        </button>
      )}
    </div>
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
  beforeEach(async () => {
    mockGetTask.mockReset()
    mockDeleteTask.mockReset()
    mockPush.mockReset()
    const { useSearchParams } = await import('next/navigation')
    vi.mocked(useSearchParams).mockReturnValue({ get: (key: string) => key === 'id' ? '1' : null } as ReturnType<typeof useSearchParams>)
  })

  it('navigates to /tasks after successful delete', async () => {
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

  it('shows error message when delete fails', async () => {
    mockGetTask.mockResolvedValue(makeTaskDetail({ id: 1, title: 'To Delete' }))
    mockDeleteTask.mockRejectedValue(new Error('delete failed'))

    render(<TaskDetailContent />)

    await waitFor(() => {
      expect(screen.getByText('Delete')).toBeTruthy()
    })

    fireEvent.click(screen.getByText('Delete'))

    await waitFor(() => {
      expect(screen.getByText('delete failed')).toBeTruthy()
    })
    expect(mockPush).not.toHaveBeenCalled()
  })
})

describe('TaskListView', () => {
  beforeEach(async () => {
    mockGetTasks.mockReset()
    const { useSearchParams } = await import('next/navigation')
    vi.mocked(useSearchParams).mockReturnValue({ get: (_key: string) => null } as ReturnType<typeof useSearchParams>)
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

  it('passes the ?status= filter to getTasks and shows a filter chip', async () => {
    const { useSearchParams } = await import('next/navigation')
    vi.mocked(useSearchParams).mockReturnValue({
      get: (key: string) => (key === 'status' ? 'skipped' : null),
    } as ReturnType<typeof useSearchParams>)
    mockGetTasks.mockResolvedValue({
      tasks: [makeTask({ id: 9, title: 'Promo', status: 'skipped' })],
      total: 1,
    })

    render(<TaskListView />)

    await waitFor(() => {
      expect(mockGetTasks).toHaveBeenCalledWith('skipped')
    })
    expect(screen.getByText('filtered by')).toBeTruthy()
  })

  it('deletes a task from the list and decrements the count', async () => {
    mockDeleteTask.mockReset()
    mockDeleteTask.mockResolvedValue(undefined)
    mockGetTasks.mockResolvedValue({
      tasks: [makeTask({ id: 1, title: 'Keep' }), makeTask({ id: 2, title: 'Remove' })],
      total: 2,
    })
    render(<TaskListView />)
    await waitFor(() => expect(screen.getByText('Remove')).toBeTruthy())

    fireEvent.click(screen.getByLabelText('delete-2'))

    await waitFor(() => expect(screen.queryByText('Remove')).toBeNull())
    expect(mockDeleteTask).toHaveBeenCalledWith(2)
    expect(screen.getByText('Keep')).toBeTruthy()
    expect(screen.getByText('(1)')).toBeTruthy()
  })

  it('keeps the task and shows an error banner when delete fails', async () => {
    mockDeleteTask.mockReset()
    mockDeleteTask.mockRejectedValue(new Error('delete boom'))
    mockGetTasks.mockResolvedValue({
      tasks: [makeTask({ id: 1, title: 'Sticky' })],
      total: 1,
    })
    render(<TaskListView />)
    await waitFor(() => expect(screen.getByText('Sticky')).toBeTruthy())

    fireEvent.click(screen.getByLabelText('delete-1'))

    await waitFor(() => expect(screen.getByText('delete boom')).toBeTruthy())
    expect(screen.getByText('Sticky')).toBeTruthy()
  })
})
