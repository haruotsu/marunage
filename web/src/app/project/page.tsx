'use client'
import { useEffect, useState, useCallback } from 'react'
import { getProject } from '@/lib/api'
import { StatusBadge } from '@/components/status-badge'
import type { ProjectResponse } from '@/lib/types'
import { RefreshCw } from 'lucide-react'
import Link from 'next/link'

export default function ProjectPage() {
  const [data, setData] = useState<ProjectResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    getProject()
      .then((resp) => {
        setData(resp)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load project')
      })
      .finally(() => setLoading(false))
  }, [tick])

  if (loading) {
    return (
      <div className="flex justify-center py-24">
        <RefreshCw className="h-6 w-6 animate-spin text-zinc-400" />
      </div>
    )
  }

  if (error || !data) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/10 dark:text-red-400">
        {error}
      </div>
    )
  }

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">
          Project
        </h1>
        <button
          onClick={refetch}
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      <div className="space-y-4">
        {data.phases.map((phase, i) => (
          <div
            key={i}
            className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900"
          >
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-semibold text-zinc-900 dark:text-zinc-100">
                {phase.name}
              </h2>
              <StatusBadge status={phase.status} />
            </div>
            {phase.items.length === 0 ? (
              <p className="text-xs text-zinc-400">No tasks in this phase</p>
            ) : (
              <ul className="space-y-1.5">
                {phase.items.map((task) => (
                  <li
                    key={task.id}
                    className="flex items-center justify-between rounded-md px-3 py-2 bg-zinc-50 dark:bg-zinc-800/50"
                  >
                    <div className="flex items-center gap-3">
                      <StatusBadge status={task.status} />
                      <Link
                        href={`/tasks?id=${task.id}`}
                        className="text-sm text-zinc-700 hover:text-blue-600 dark:text-zinc-300 dark:hover:text-blue-400"
                      >
                        {task.title}
                      </Link>
                    </div>
                    <span className="text-xs text-zinc-400">#{task.id}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
