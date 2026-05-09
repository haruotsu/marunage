import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

let _cachedCsrfToken = ''

export function setCsrfToken(token: string): void {
  _cachedCsrfToken = token
}

export function getCsrfToken(): string {
  if (_cachedCsrfToken) return _cachedCsrfToken
  if (typeof document === 'undefined') return ''
  return document.cookie.match(/marunage_csrf=([^;]+)/)?.[1] ?? ''
}

export function formatRelativeTime(iso: string | null): string {
  if (!iso) return '--'
  const date = new Date(iso)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffSec = Math.floor(diffMs / 1000)
  if (diffSec < 60) return `${diffSec}s ago`
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  return `${diffDay}d ago`
}

export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  const mins = Math.floor(seconds / 60)
  const secs = Math.round(seconds % 60)
  return `${mins}m ${secs}s`
}
