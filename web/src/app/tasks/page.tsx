'use client'
import { useEffect, useState, useCallback, Suspense } from 'react'
import { useSearchParams } from 'next/navigation'
import { getTask, dispatchTask, promoteTask, reopenTask, deleteTask } from '@/lib/api'
import { StatusBadge } from '@/components/status-badge'
import { LiveStream } from '@/components/live-stream'
import { formatRelativeTime } from '@/lib/utils'
import type { TaskDetail } from '@/lib/types'
import { ArrowLeft, ExternalLink, RefreshCw, Trash2 } from 'lucide-react'
import Link from 'next/link'

export default function TaskDetailPage() {
  return (
    <Suspense fallback={<div className="flex justify-center py-24"><RefreshCw className="h-6 w-6 animate-spin text-zinc-400" /></div>}>
      <TaskDetailContent />
    </Suspense>
  )
}

function TaskDetailContent() {
  const params = useSearchParams()
  const idStr = params.get('id')
  const id = idStr ? parseInt(idStr, 10) : null

  const [task, setTask] = useState<TaskDetail | null>(null)
  const [loading, setLoading] = useState(id !== null)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    if (!id) return
    let active = true
    getTask(id)
      .then((t) => {
        if (!active) return
        setTask(t)
        setError(null)
        setLoading(false)
      })
      .catch((e: unknown) => {
        if (!active) return
        setError(e instanceof Error ? e.message : 'Failed to load task')
        setLoading(false)
      })
    return () => { active = false }
  }, [id, tick])

  function doAction(fn: () => Promise<void>) {
    setActionError(null)
    fn()
      .then(refetch)
      .catch((e: unknown) => {
        setActionError(e instanceof Error ? e.message : 'Action failed')
      })
  }

  if (!id) {
    return (
      <div className="py-12 text-center text-sm text-zinc-500">
        No task selected. Use <code className="font-mono">?id=N</code> query param.
      </div>
    )
  }

  if (loading && !task) {
    return (
      <div className="flex justify-center py-24">
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

  if (!task) return null

  return (
    <div>
      <div className="mb-4 flex items-center gap-3">
        <Link
          href="/"
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <ArrowLeft className="h-4 w-4" />
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <StatusBadge status={task.status} />
            <span className="text-xs text-zinc-500">#{task.id}</span>
            <span className="text-xs text-zinc-500">{task.source}</span>
          </div>
          <h1 className="mt-1 text-lg font-semibold text-zinc-900 dark:text-zinc-100">
            {task.title}
          </h1>
        </div>
        <button
          onClick={refetch}
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      {actionError && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/10 dark:text-red-400">
          {actionError}
        </div>
      )}

      <div className="mb-4 flex flex-wrap gap-2">
        {task.status === 'pending' && (
          <ActionButton
            label="Dispatch"
            onClick={() => doAction(() => dispatchTask(task.id))}
            variant="primary"
          />
        )}
        <ActionButton
          label="Promote"
          onClick={() => doAction(() => promoteTask(task.id))}
        />
        {(task.status === 'done' || task.status === 'failed' || task.status === 'skipped') && (
          <ActionButton
            label="Reopen"
            onClick={() => doAction(() => reopenTask(task.id))}
          />
        )}
        <ActionButton
          label="Delete"
          onClick={() => doAction(() => deleteTask(task.id))}
          variant="danger"
          icon={<Trash2 className="h-3.5 w-3.5" />}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title="Details">
          <FieldRow label="External ID" value={task.external_id} />
          {task.external_url && (
            <div className="flex items-start justify-between py-2 border-b border-zinc-100 dark:border-zinc-800 last:border-0">
              <span className="text-xs text-zinc-500 w-28 shrink-0">External URL</span>
              <a
                href={task.external_url}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 text-xs text-blue-600 hover:underline dark:text-blue-400 text-right break-all"
              >
                {task.external_url}
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </div>
          )}
          <FieldRow label="Priority" value={String(task.priority)} />
          <FieldRow label="Lock Key" value={task.lock_key || '--'} />
          <FieldRow label="CWD" value={task.cwd || '--'} mono />
          <FieldRow label="Workspace" value={task.ws || '--'} mono />
          <FieldRow label="Judgment" value={task.judgment_reason || '--'} />
        </Section>

        <Section title="Timestamps">
          <FieldRow label="Created" value={`${task.created_at} (${formatRelativeTime(task.created_at)})`} />
          <FieldRow label="Updated" value={`${task.updated_at} (${formatRelativeTime(task.updated_at)})`} />
          <FieldRow
            label="Started"
            value={task.started_at ? `${task.started_at} (${formatRelativeTime(task.started_at)})` : '--'}
          />
          <FieldRow
            label="Completed"
            value={task.completed_at ? `${task.completed_at} (${formatRelativeTime(task.completed_at)})` : '--'}
          />
        </Section>
      </div>

      {task.body && (
        <Section title="Body" className="mt-4">
          <pre className="whitespace-pre-wrap text-xs text-zinc-700 dark:text-zinc-300 leading-relaxed">
            {task.body}
          </pre>
        </Section>
      )}

      {task.notes && (
        <Section title="Notes" className="mt-4">
          <pre className="whitespace-pre-wrap text-xs text-zinc-700 dark:text-zinc-300 leading-relaxed">
            {task.notes}
          </pre>
        </Section>
      )}

      {task.result_summary && (
        <Section title="Result Summary" className="mt-4">
          <pre className="whitespace-pre-wrap text-xs text-zinc-700 dark:text-zinc-300 leading-relaxed">
            {task.result_summary}
          </pre>
        </Section>
      )}

      {task.reflection && (
        <Section title="Reflection" className="mt-4">
          <pre className="whitespace-pre-wrap text-xs text-zinc-700 dark:text-zinc-300 leading-relaxed">
            {task.reflection}
          </pre>
        </Section>
      )}

      <div className="mt-4">
        <h2 className="mb-2 text-sm font-semibold text-zinc-700 dark:text-zinc-300">Terminal Stream</h2>
        <LiveStream taskId={task.id} active={task.status === 'running'} />
      </div>

      {task.audit_entries.length > 0 && (
        <Section title="Audit Log" className="mt-4">
          <div className="space-y-2">
            {task.audit_entries.map((entry) => (
              <div key={`${entry.time}-${entry.action}`} className="flex gap-3 text-xs">
                <span className="shrink-0 text-zinc-400 font-mono w-32">
                  {formatRelativeTime(entry.time)}
                </span>
                <span className="shrink-0 font-medium text-zinc-600 dark:text-zinc-400 w-20">
                  {entry.action}
                </span>
                <span className="text-zinc-600 dark:text-zinc-400 break-all">
                  {entry.value}
                </span>
              </div>
            ))}
          </div>
        </Section>
      )}
    </div>
  )
}

function Section({
  title,
  children,
  className = '',
}: {
  title: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={`rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900 ${className}`}>
      <h2 className="mb-2 text-sm font-semibold text-zinc-700 dark:text-zinc-300">{title}</h2>
      {children}
    </div>
  )
}

function FieldRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-2 py-1.5 border-b border-zinc-100 dark:border-zinc-800 last:border-0">
      <span className="text-xs text-zinc-500 w-28 shrink-0">{label}</span>
      <span className={`text-xs text-zinc-800 dark:text-zinc-200 text-right break-all ${mono ? 'font-mono' : ''}`}>
        {value}
      </span>
    </div>
  )
}

function ActionButton({
  label,
  onClick,
  variant = 'default',
  icon,
}: {
  label: string
  onClick: () => void
  variant?: 'primary' | 'default' | 'danger'
  icon?: React.ReactNode
}) {
  const cls = {
    primary: 'bg-blue-600 text-white hover:bg-blue-700',
    default: 'bg-zinc-100 text-zinc-700 hover:bg-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-700',
    danger: 'bg-red-50 text-red-700 hover:bg-red-100 dark:bg-red-900/20 dark:text-red-400 dark:hover:bg-red-900/30',
  }[variant]

  return (
    <button onClick={onClick} className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium ${cls}`}>
      {icon}
      {label}
    </button>
  )
}
