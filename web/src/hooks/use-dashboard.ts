'use client'
import { useState, useEffect, useCallback } from 'react'
import { getDashboard } from '@/lib/api'
import type { DashboardSnapshot } from '@/lib/types'

export function useDashboard(intervalMs = 5000) {
  const [data, setData] = useState<DashboardSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => {
    setTick((n) => n + 1)
  }, [])

  useEffect(() => {
    getDashboard()
      .then((snap) => {
        setData(snap)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load dashboard')
      })
      .finally(() => {
        setLoading(false)
      })
  }, [tick])

  useEffect(() => {
    const id = setInterval(() => setTick((n) => n + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])

  return { data, error, loading, refetch }
}
