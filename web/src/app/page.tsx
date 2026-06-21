'use client'
import { useState } from 'react'
import { useDashboard } from '@/hooks/use-dashboard'
import { StatusBadge } from '@/components/status-badge'
import { AddTaskModal } from '@/components/add-task-modal'
import { formatRelativeTime } from '@/lib/utils'
import { dispatchTask, promoteTask, deleteTask } from '@/lib/api'
import { Plus, RefreshCw, Terminal, Trash2 } from 'lucide-react'
import Link from 'next/link'

export default function DashboardPage() {
  const { data, error, loading, refetch } = useDashboard(5000)
  const [showAddModal, setShowAddModal] = useState(false)

  async function handleDispatch(id: number) {
    await dispatchTask(id)
    refetch()
  }

  async function handlePromote(id: number) {
    await promoteTask(id)
    refetch()
  }

  async function handleDelete(id: number, title: string) {
    if (!window.confirm(`Delete task "${title}"? This cannot be undone.`)) return
    await deleteTask(id)
    refetch()
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center py-24">
        <RefreshCw className="h-6 w-6 animate-spin text-zinc-400" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/10 dark:text-red-400">
        {error}
      </div>
    )
  }

  const snap = data!

  return (
    <>
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">
            Dashboard
          </h1>
          <p className="text-xs text-zinc-500 mt-0.5">
            Updated {formatRelativeTime(snap.generated_at)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={refetch}
            className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
          >
            <RefreshCw className="h-4 w-4" />
          </button>
          <button
            onClick={() => setShowAddModal(true)}
            className="flex items-center gap-2 rounded-lg bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-700"
          >
            <Plus className="h-4 w-4" />
            Add Task
          </button>
        </div>
      </div>

      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <StatCard
          label="Running"
          value={snap.running.length}
          color="text-blue-600"
          href="/tasks?status=running"
        />
        <StatCard
          label="Pending"
          value={snap.pending_count}
          color="text-zinc-600"
          href="/tasks?status=pending"
        />
        <StatCard
          label="Done (24h)"
          value={snap.recent_24h.done_count}
          color="text-green-600"
          href="/tasks?status=done"
        />
        <StatCard
          label="Failed (24h)"
          value={snap.recent_24h.failed_count}
          color="text-red-600"
          href="/tasks?status=failed"
        />
        <StatCard
          label="Skipped (24h)"
          value={snap.recent_24h.skipped_count}
          color="text-amber-600"
          href="/tasks?status=skipped"
        />
      </div>

      <div className="mb-6">
        <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">
          Running Tasks
        </h2>
        {snap.running.length === 0 ? (
          <EmptyState message="No running tasks" />
        ) : (
          <div className="space-y-2">
            {snap.running.map((t) => (
              <div
                key={t.id}
                className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900"
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <StatusBadge status="running" />
                      <span className="text-xs text-zinc-500">{t.source}</span>
                    </div>
                    <Link
                      href={`/tasks?id=${t.id}`}
                      className="mt-1 block truncate text-sm font-medium text-zinc-900 hover:text-blue-600 dark:text-zinc-100"
                    >
                      {t.title}
                    </Link>
                    <div className="mt-1 flex items-center gap-3 text-xs text-zinc-500">
                      <span>Started {formatRelativeTime(t.started_at)}</span>
                      {t.ws && (
                        <span className="font-mono">{t.ws}</span>
                      )}
                    </div>
                    {t.output_preview && (
                      <pre className="mt-2 rounded bg-zinc-950 p-2 text-xs text-zinc-300 font-mono overflow-hidden max-h-16 leading-relaxed">
                        {t.output_preview}
                      </pre>
                    )}
                  </div>
                  <Terminal className="h-4 w-4 shrink-0 text-zinc-400" />
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="mb-6">
        <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">
          Pending Queue
        </h2>
        {snap.pending.length === 0 ? (
          <EmptyState message="No pending tasks" />
        ) : (
          <div className="space-y-2">
            {snap.pending.map((t) => (
              <div
                key={t.id}
                className="flex items-center justify-between rounded-lg border border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-900"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-mono text-zinc-400">
                      P{t.priority}
                    </span>
                    <span className="text-xs text-zinc-500">{t.source}</span>
                  </div>
                  <Link
                    href={`/tasks?id=${t.id}`}
                    className="block truncate text-sm text-zinc-800 hover:text-blue-600 dark:text-zinc-200"
                  >
                    {t.title}
                  </Link>
                  <span className="text-xs text-zinc-400">
                    {formatRelativeTime(t.created_at)}
                  </span>
                </div>
                <div className="ml-3 flex gap-1">
                  <button
                    onClick={() => handleDispatch(t.id)}
                    className="rounded px-2 py-1 text-xs bg-blue-50 text-blue-700 hover:bg-blue-100 dark:bg-blue-900/20 dark:text-blue-400"
                  >
                    Dispatch
                  </button>
                  <button
                    onClick={() => handlePromote(t.id)}
                    className="rounded px-2 py-1 text-xs bg-zinc-50 text-zinc-600 hover:bg-zinc-100 dark:bg-zinc-800 dark:text-zinc-300"
                  >
                    Promote
                  </button>
                  <button
                    onClick={() => handleDelete(t.id, t.title)}
                    aria-label="Delete task"
                    title="Delete task"
                    className="rounded px-2 py-1 text-xs bg-red-50 text-red-600 hover:bg-red-100 dark:bg-red-900/20 dark:text-red-400"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div>
        <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">
          Source Status
        </h2>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {snap.sources.map((src) => (
            <div
              key={src.name}
              className="flex items-center justify-between rounded-lg border border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-900"
            >
              <span className="text-sm font-medium text-zinc-800 dark:text-zinc-200">
                {src.name}
              </span>
              <div className="flex items-center gap-2">
                <AuthBadge status={src.auth_status} />
                <span className="text-xs text-zinc-400">
                  {src.last_listed_at
                    ? formatRelativeTime(src.last_listed_at)
                    : '--'}
                </span>
              </div>
            </div>
          ))}
        </div>
      </div>

      <AddTaskModal
        open={showAddModal}
        onClose={() => setShowAddModal(false)}
        onAdded={refetch}
      />
    </>
  )
}

function StatCard({
  label,
  value,
  color,
  href,
}: {
  label: string
  value: number
  color: string
  href: string
}) {
  return (
    <Link
      href={href}
      className="group rounded-lg border border-zinc-200 bg-white p-4 transition hover:border-blue-300 hover:shadow-sm dark:border-zinc-800 dark:bg-zinc-900 dark:hover:border-blue-700"
    >
      <p className="text-xs text-zinc-500 group-hover:text-blue-600 dark:group-hover:text-blue-400">
        {label}
      </p>
      <p className={`mt-1 text-2xl font-bold ${color}`}>{value}</p>
    </Link>
  )
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="rounded-lg border border-dashed border-zinc-300 bg-white py-8 text-center dark:border-zinc-700 dark:bg-zinc-900">
      <p className="text-sm text-zinc-400">{message}</p>
    </div>
  )
}

function AuthBadge({
  status,
}: {
  status: 'authenticated' | 'expired' | 'revoked' | 'not_configured' | string
}) {
  const config: Record<string, { label: string; className: string }> = {
    authenticated: {
      label: 'OK',
      className: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
    },
    expired: {
      label: 'Expired',
      className: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400',
    },
    revoked: {
      label: 'Revoked',
      className: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
    },
    not_configured: {
      label: 'Not set',
      className: 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
    },
  }
  const c = config[status] ?? config['not_configured']
  return (
    <span
      className={`rounded-full px-2 py-0.5 text-xs font-medium ${c.className}`}
    >
      {c.label}
    </span>
  )
}
