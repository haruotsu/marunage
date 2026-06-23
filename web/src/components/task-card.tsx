'use client'
import Link from 'next/link'
import { StatusBadge } from './status-badge'
import { formatRelativeTime } from '@/lib/utils'
import type { Task } from '@/lib/types'

interface TaskCardProps {
  task: Task
  onDispatch?: (id: number) => void
  onPromote?: (id: number) => void
  onReopen?: (id: number) => void
  onDelete?: (id: number) => void
}

export function TaskCard({ task, onDispatch, onPromote, onReopen, onDelete }: TaskCardProps) {
  return (
    <div className="rounded-lg border border-zinc-200 bg-white p-4 shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <StatusBadge status={task.status} />
            <span className="text-xs text-zinc-500">{task.source}</span>
            {task.priority > 0 && (
              <span className="text-xs text-zinc-500">P{task.priority}</span>
            )}
          </div>
          <Link
            href={`/tasks?id=${task.id}`}
            className="mt-1 block truncate text-sm font-medium text-zinc-900 hover:text-blue-600 dark:text-zinc-100 dark:hover:text-blue-400"
          >
            {task.title}
          </Link>
          <div className="mt-1 flex items-center gap-3 text-xs text-zinc-500">
            <span>#{task.id}</span>
            <span>{formatRelativeTime(task.created_at)}</span>
            {task.ws && <span className="font-mono">{task.ws}</span>}
          </div>
        </div>
        <div className="flex shrink-0 gap-1">
          {onDispatch && task.status === 'pending' && (
            <button
              onClick={() => onDispatch(task.id)}
              className="rounded px-2 py-1 text-xs bg-blue-50 text-blue-700 hover:bg-blue-100 dark:bg-blue-900/20 dark:text-blue-400 dark:hover:bg-blue-900/40"
            >
              Dispatch
            </button>
          )}
          {onPromote && (
            <button
              onClick={() => onPromote(task.id)}
              className="rounded px-2 py-1 text-xs bg-zinc-50 text-zinc-700 hover:bg-zinc-100 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-700"
            >
              Promote
            </button>
          )}
          {onReopen && (task.status === 'done' || task.status === 'failed' || task.status === 'skipped') && (
            <button
              onClick={() => onReopen(task.id)}
              className="rounded px-2 py-1 text-xs bg-zinc-50 text-zinc-700 hover:bg-zinc-100 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-700"
            >
              Reopen
            </button>
          )}
          {onDelete && (
            <button
              onClick={() => {
                if (window.confirm(`Delete task #${task.id}? This cannot be undone.`)) {
                  onDelete(task.id)
                }
              }}
              aria-label={`Delete task #${task.id}`}
              title="Delete"
              className="rounded px-2 py-1 text-xs bg-red-50 text-red-700 hover:bg-red-100 dark:bg-red-900/20 dark:text-red-400 dark:hover:bg-red-900/40"
            >
              Delete
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
