'use client'
import { useEffect, useState, useCallback } from 'react'
import { getMetrics } from '@/lib/api'
import { formatDuration } from '@/lib/utils'
import type { MetricsSnapshot } from '@/lib/types'
import { RefreshCw } from 'lucide-react'
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  PieChart,
  Pie,
  Cell,
  LineChart,
  Line,
  CartesianGrid,
  Legend,
  type PieLabelRenderProps,
} from 'recharts'

const STATUS_COLORS: Record<string, string> = {
  done: '#22c55e',
  failed: '#ef4444',
  skipped: '#eab308',
  pending: '#71717a',
  running: '#3b82f6',
  waiting_human: '#f97316',
}

const PIE_COLORS = ['#3b82f6', '#22c55e', '#f97316', '#a855f7', '#ec4899']

export default function MetricsPage() {
  const [data, setData] = useState<MetricsSnapshot | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    getMetrics()
      .then((snap) => {
        setData(snap)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load metrics')
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

  const byStatusData = Object.entries(data.by_status).map(([status, count]) => ({
    status,
    count,
    fill: STATUS_COLORS[status] ?? '#71717a',
  }))

  const bySourceData = Object.entries(data.by_source).map(([name, count]) => ({
    name,
    value: count,
  }))

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">
          Metrics
        </h1>
        <button
          onClick={refetch}
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <MetricCard label="Total Tasks" value={String(data.total_tasks)} />
        <MetricCard
          label="Success Rate"
          value={`${(data.success_rate * 100).toFixed(1)}%`}
          color="text-green-600"
        />
        <MetricCard
          label="Avg Duration"
          value={formatDuration(data.avg_duration_seconds)}
        />
        <MetricCard
          label="Done"
          value={String(data.by_status['done'] ?? 0)}
          color="text-green-600"
        />
      </div>

      <div className="mb-6 grid gap-6 lg:grid-cols-2">
        <ChartCard title="By Status">
          <ResponsiveContainer width="100%" height={220}>
            <BarChart data={byStatusData} margin={{ top: 0, right: 8, bottom: 0, left: -16 }}>
              <XAxis dataKey="status" tick={{ fontSize: 11 }} />
              <YAxis tick={{ fontSize: 11 }} />
              <Tooltip />
              <Bar dataKey="count" radius={[3, 3, 0, 0]}>
                {byStatusData.map((entry, i) => (
                  <Cell key={i} fill={entry.fill} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </ChartCard>

        <ChartCard title="By Source">
          {bySourceData.length === 0 ? (
            <div className="flex h-48 items-center justify-center text-sm text-zinc-400">
              No data
            </div>
          ) : (
            <ResponsiveContainer width="100%" height={220}>
              <PieChart>
                <Pie
                  data={bySourceData}
                  cx="50%"
                  cy="50%"
                  innerRadius={50}
                  outerRadius={80}
                  paddingAngle={3}
                  dataKey="value"
                  label={({ name, percent }: PieLabelRenderProps) =>
                    `${name ?? ''} ${(((percent as number | undefined) ?? 0) * 100).toFixed(0)}%`
                  }
                  labelLine={false}
                >
                  {bySourceData.map((_, i) => (
                    <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                  ))}
                </Pie>
                <Tooltip />
              </PieChart>
            </ResponsiveContainer>
          )}
        </ChartCard>
      </div>

      <ChartCard title="Daily Tasks (Last 30 Days)">
        {data.daily_counts.length === 0 ? (
          <div className="flex h-48 items-center justify-center text-sm text-zinc-400">
            No data
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={220}>
            <LineChart
              data={data.daily_counts}
              margin={{ top: 0, right: 8, bottom: 0, left: -16 }}
            >
              <CartesianGrid strokeDasharray="3 3" stroke="#27272a" />
              <XAxis
                dataKey="date"
                tick={{ fontSize: 10 }}
                tickFormatter={(v: string) => v.slice(5)}
              />
              <YAxis tick={{ fontSize: 11 }} />
              <Tooltip />
              <Legend />
              <Line
                type="monotone"
                dataKey="done"
                stroke="#22c55e"
                dot={false}
                strokeWidth={2}
              />
              <Line
                type="monotone"
                dataKey="failed"
                stroke="#ef4444"
                dot={false}
                strokeWidth={2}
              />
            </LineChart>
          </ResponsiveContainer>
        )}
      </ChartCard>
    </div>
  )
}

function MetricCard({
  label,
  value,
  color = 'text-zinc-900 dark:text-zinc-100',
}: {
  label: string
  value: string
  color?: string
}) {
  return (
    <div className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <p className="text-xs text-zinc-500">{label}</p>
      <p className={`mt-1 text-2xl font-bold ${color}`}>{value}</p>
    </div>
  )
}

function ChartCard({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">
        {title}
      </h2>
      {children}
    </div>
  )
}
