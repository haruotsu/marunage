import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { Task } from '@/lib/types'
import { TaskCard } from './task-card'

vi.mock('./status-badge', () => ({
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

describe('TaskCard delete', () => {
  it('does not render delete button when onDelete is not provided', () => {
    render(<TaskCard task={makeTask()} />)
    expect(screen.queryByLabelText('Delete task')).toBeNull()
  })

  it('renders delete button when onDelete is provided', () => {
    render(<TaskCard task={makeTask()} onDelete={() => {}} />)
    expect(screen.getByLabelText('Delete task')).toBeTruthy()
  })

  it('calls onDelete with task.id when delete button is clicked and confirmed', () => {
    const onDelete = vi.fn()
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    render(<TaskCard task={makeTask({ id: 42 })} onDelete={onDelete} />)
    fireEvent.click(screen.getByLabelText('Delete task'))
    expect(onDelete).toHaveBeenCalledWith(42)
    confirmSpy.mockRestore()
  })

  it('does not call onDelete when confirmation is declined', () => {
    const onDelete = vi.fn()
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false)
    render(<TaskCard task={makeTask({ id: 42 })} onDelete={onDelete} />)
    fireEvent.click(screen.getByLabelText('Delete task'))
    expect(onDelete).not.toHaveBeenCalled()
    confirmSpy.mockRestore()
  })

})
