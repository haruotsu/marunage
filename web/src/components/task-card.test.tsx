import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { TaskCard } from './task-card'
import type { Task } from '@/lib/types'

vi.mock('./status-badge', () => ({ StatusBadge: () => null }))
vi.mock('next/link', () => ({
  default: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}))

function makeTask(overrides: Partial<Task> = {}): Task {
  const id = overrides.id ?? 1
  return {
    id,
    source: 'github',
    external_id: `ext-${id}`,
    external_url: '',
    title: 'Delete me',
    body: '',
    notes: '',
    status: 'pending',
    judgment_reason: '',
    priority: 0,
    ws: '',
    result_summary: '',
    reflection: '',
    created_at: '2025-01-01T00:00:00Z',
    updated_at: '2025-01-01T00:00:00Z',
    started_at: null,
    completed_at: null,
    ...overrides,
  }
}

describe('TaskCard delete action', () => {
  afterEach(() => vi.restoreAllMocks())

  it('renders no delete button when onDelete is not provided', () => {
    render(<TaskCard task={makeTask()} />)
    expect(screen.queryByRole('button', { name: /delete/i })).toBeNull()
  })

  it('calls onDelete with the task id after the user confirms', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const onDelete = vi.fn()
    render(<TaskCard task={makeTask({ id: 42 })} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole('button', { name: /delete/i }))
    expect(onDelete).toHaveBeenCalledWith(42)
  })

  it('does not call onDelete when the user cancels the confirm dialog', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const onDelete = vi.fn()
    render(<TaskCard task={makeTask({ id: 42 })} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole('button', { name: /delete/i }))
    expect(onDelete).not.toHaveBeenCalled()
  })
})
