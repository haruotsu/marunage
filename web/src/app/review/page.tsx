'use client'
import { useEffect, useState, useCallback } from 'react'
import { getSkippedTasks, promoteTask } from '@/lib/api'
import { StatusBadge } from '@/components/status-badge'
import { formatRelativeTime } from '@/lib/utils'
import type { Task } from '@/lib/types'
import { RefreshCw } from 'lucide-react'
import Link from 'next/link'

export default function ReviewPage() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filterStart, setFilterStart] = useState('')
  const [filterEnd, setFilterEnd] = useState('')
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    getSkippedTasks()
      .then((list) => {
        setTasks(list)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load skipped tasks')
      })
      .finally(() => setLoading(false))
  }, [tick])

  function handlePromote(id: number) {
    promoteTask(id).then(refetch).catch((e: unknown) => {
      setError(e instanceof Error ? e.message : 'Failed to promote')
    })
  }

  const filtered = tasks.filter((t) => {
    if (filterStart && t.created_at < filterStart) return false
    if (filterEnd && t.created_at > filterEnd + 'T23:59:59') return false
    return true
  })

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">
          Review
        </h1>
        <button
          onClick={refetch}
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2">
          <label className="text-xs text-zinc-500">From</label>
          <input
            type="date"
            value={filterStart}
            onChange={(e) => setFilterStart(e.target.value)}
            className="rounded-lg border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-900 focus:border-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
          />
        </div>
        <div className="flex items-center gap-2">
          <label className="text-xs text-zinc-500">To</label>
          <input
            type="date"
            value={filterEnd}
            onChange={(e) => setFilterEnd(e.target.value)}
            className="rounded-lg border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-900 focus:border-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
          />
        </div>
        {(filterStart || filterEnd) && (
          <button
            onClick={() => { setFilterStart(''); setFilterEnd('') }}
            className="text-xs text-zinc-500 hover:text-zinc-700 dark:hover:text-zinc-300"
          >
            Clear
          </button>
        )}
      </div>

      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/10 dark:text-red-400">
          {error}
        </div>
      )}

      {loading ? (
        <div className="flex justify-center py-24">
          <RefreshCw className="h-6 w-6 animate-spin text-zinc-400" />
        </div>
      ) : filtered.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-300 bg-white py-12 text-center dark:border-zinc-700 dark:bg-zinc-900">
          <p className="text-sm text-zinc-400">No skipped tasks</p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 dark:border-zinc-800">
                <th className="px-4 py-3 text-left text-xs font-medium text-zinc-500 uppercase">ID</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-zinc-500 uppercase">Title</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-zinc-500 uppercase">Source</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-zinc-500 uppercase">Status</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-zinc-500 uppercase">Created</th>
                <th className="px-4 py-3 text-right text-xs font-medium text-zinc-500 uppercase">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((task) => (
                <tr key={task.id} className="border-b border-zinc-100 last:border-0 dark:border-zinc-800">
                  <td className="px-4 py-3 text-xs text-zinc-400">#{task.id}</td>
                  <td className="px-4 py-3 max-w-xs">
                    <Link
                      href={`/tasks?id=${task.id}`}
                      className="text-zinc-800 hover:text-blue-600 dark:text-zinc-200 dark:hover:text-blue-400 truncate block"
                    >
                      {task.title}
                    </Link>
                    {task.judgment_reason && (
                      <p className="mt-0.5 text-xs text-zinc-400 truncate">{task.judgment_reason}</p>
                    )}
                  </td>
                  <td className="px-4 py-3 text-xs text-zinc-500">{task.source}</td>
                  <td className="px-4 py-3"><StatusBadge status={task.status} /></td>
                  <td className="px-4 py-3 text-xs text-zinc-400">{formatRelativeTime(task.created_at)}</td>
                  <td className="px-4 py-3 text-right">
                    <button
                      onClick={() => handlePromote(task.id)}
                      className="rounded px-2 py-1 text-xs bg-blue-50 text-blue-700 hover:bg-blue-100 dark:bg-blue-900/20 dark:text-blue-400"
                    >
                      Promote
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
