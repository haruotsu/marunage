'use client'
import { useEffect, useState, useCallback } from 'react'
import { getJournal } from '@/lib/api'
import type { JournalEntry } from '@/lib/types'
import { RefreshCw } from 'lucide-react'
import Link from 'next/link'

function todayStr() {
  return new Date().toISOString().slice(0, 10)
}

export default function JournalPage() {
  const [date, setDate] = useState(todayStr())
  const [entries, setEntries] = useState<JournalEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    getJournal(date)
      .then((list) => {
        setEntries(list)
        setError(null)
        setLoading(false)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load journal')
        setLoading(false)
      })
  }, [date, tick])

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">Journal</h1>
        <div className="flex items-center gap-2">
          <input
            type="date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
            className="rounded-lg border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-900 focus:border-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
          />
          <button
            onClick={refetch}
            className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
          >
            <RefreshCw className="h-4 w-4" />
          </button>
        </div>
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
      ) : entries.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-300 bg-white py-12 text-center dark:border-zinc-700 dark:bg-zinc-900">
          <p className="text-sm text-zinc-400">No journal entries for {date}</p>
        </div>
      ) : (
        <div className="relative">
          <div className="absolute left-[88px] top-0 bottom-0 w-px bg-zinc-200 dark:bg-zinc-800" />
          <div className="space-y-4">
            {entries.map((entry, i) => (
              <div key={i} className="flex gap-6">
                <div className="w-20 shrink-0 text-right">
                  <span className="text-xs text-zinc-400 font-mono">
                    {new Date(entry.timestamp).toLocaleTimeString([], {
                      hour: '2-digit',
                      minute: '2-digit',
                    })}
                  </span>
                </div>
                <div className="relative flex-1 pb-4">
                  <div className="absolute -left-[27px] top-1 h-2.5 w-2.5 rounded-full border-2 border-white bg-zinc-400 dark:border-zinc-950 dark:bg-zinc-600" />
                  <div className="rounded-lg border border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-900">
                    <div className="flex items-center gap-2 mb-1">
                      <span className="text-xs font-medium text-zinc-600 dark:text-zinc-400">
                        {entry.source}
                      </span>
                      {entry.task_id && (
                        <Link
                          href={`/tasks?id=${entry.task_id}`}
                          className="text-xs text-blue-600 hover:underline dark:text-blue-400"
                        >
                          #{entry.task_id}
                        </Link>
                      )}
                    </div>
                    <p className="text-sm text-zinc-800 dark:text-zinc-200">{entry.summary}</p>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
