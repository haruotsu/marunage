import { describe, it, expect, beforeEach } from 'vitest'
import { getCsrfToken, setCsrfToken, formatRelativeTime, formatDuration } from './utils'

describe('getCsrfToken', () => {
  beforeEach(() => {
    setCsrfToken('')
    document.cookie.split(';').forEach((c) => {
      document.cookie = c.trim().split('=')[0] + '=;expires=Thu, 01 Jan 1970 00:00:00 GMT'
    })
  })

  it('returns empty string when cookie is absent', () => {
    expect(getCsrfToken()).toBe('')
  })

  it('extracts the marunage_csrf cookie value', () => {
    document.cookie = 'marunage_csrf=abc123def'
    expect(getCsrfToken()).toBe('abc123def')
  })

  it('ignores unrelated cookies', () => {
    document.cookie = 'other=xyz'
    document.cookie = 'marunage_csrf=tok42'
    expect(getCsrfToken()).toBe('tok42')
  })

  it('does NOT match the old csrf_token cookie name', () => {
    document.cookie = 'csrf_token=wrong'
    expect(getCsrfToken()).toBe('')
  })
})

describe('setCsrfToken / getCsrfToken cache', () => {
  beforeEach(() => {
    setCsrfToken('')
    document.cookie.split(';').forEach((c) => {
      document.cookie = c.trim().split('=')[0] + '=;expires=Thu, 01 Jan 1970 00:00:00 GMT'
    })
  })

  it('returns cached token instead of reading cookie', () => {
    document.cookie = 'marunage_csrf=cookie-value'
    setCsrfToken('cached-value')
    expect(getCsrfToken()).toBe('cached-value')
  })

  it('falls back to cookie after cache is cleared', () => {
    document.cookie = 'marunage_csrf=cookie-value'
    setCsrfToken('cached-value')
    setCsrfToken('')
    expect(getCsrfToken()).toBe('cookie-value')
  })

  it('cache does not leak between tests', () => {
    expect(getCsrfToken()).toBe('')
  })
})

describe('formatRelativeTime', () => {
  it('returns -- for null input', () => {
    expect(formatRelativeTime(null)).toBe('--')
  })

  it('returns seconds for very recent times', () => {
    const recent = new Date(Date.now() - 5000).toISOString()
    expect(formatRelativeTime(recent)).toMatch(/^\d+s ago$/)
  })
})

describe('formatDuration', () => {
  it('formats sub-minute durations in seconds', () => {
    expect(formatDuration(45)).toBe('45s')
  })

  it('formats minute-range durations', () => {
    expect(formatDuration(90)).toBe('1m 30s')
  })
})
