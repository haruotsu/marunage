import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import type { Task } from '@/lib/types'
import { TaskListView } from './page'

const mockGetTasks = vi.fn()

vi.mock('@/lib/api', () => ({
  getTasks: () => mockGetTasks(),
  getTask: vi.fn(),
  dispatchTask: vi.fn(),
  promoteTask: vi.fn(),
  reopenTask: vi.fn(),
  deleteTask: vi.fn(),
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
  return {
    id: 1,
    source: 'github',
    external_id: 'ext-1',
    external_url: 'https://example.com/1',
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
