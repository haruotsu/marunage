'use client'
import { useEffect, useState, useCallback } from 'react'
import { getInstalledSkills, searchSkillsRegistry } from '@/lib/api'
import { formatRelativeTime } from '@/lib/utils'
import type { SkillInfo, SkillRegistryEntry } from '@/lib/types'
import { RefreshCw, Search, Puzzle } from 'lucide-react'

export default function SkillsPage() {
  const [installed, setInstalled] = useState<SkillInfo[]>([])
  const [registry, setRegistry] = useState<SkillRegistryEntry[]>([])
  const [query, setQuery] = useState('')
  const [searching, setSearching] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    getInstalledSkills()
      .then((skills) => {
        setInstalled(skills)
        setError(null)
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : 'Failed to load skills')
      })
      .finally(() => setLoading(false))

    searchSkillsRegistry('').then(setRegistry).catch(() => {})
  }, [tick])

  function handleSearch(e: React.FormEvent) {
    e.preventDefault()
    setSearching(true)
    searchSkillsRegistry(query)
      .then(setRegistry)
      .catch(() => {})
      .finally(() => setSearching(false))
  }

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-900 dark:text-zinc-100">Skills</h1>
        <button
          onClick={refetch}
          className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
        >
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/10 dark:text-red-400">
          {error}
        </div>
      )}

      <div className="mb-6">
        <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">Installed Skills</h2>
        {loading ? (
          <div className="flex justify-center py-8">
            <RefreshCw className="h-5 w-5 animate-spin text-zinc-400" />
          </div>
        ) : installed.length === 0 ? (
          <div className="rounded-lg border border-dashed border-zinc-300 bg-white py-8 text-center dark:border-zinc-700 dark:bg-zinc-900">
            <Puzzle className="mx-auto h-8 w-8 text-zinc-300 dark:text-zinc-600" />
            <p className="mt-2 text-sm text-zinc-400">No skills installed</p>
          </div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {installed.map((skill) => (
              <div key={skill.name} className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
                <div className="flex items-start justify-between">
                  <div>
                    <p className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{skill.name}</p>
                    <p className="mt-0.5 text-xs text-zinc-500">{skill.description}</p>
                  </div>
                  <span className="ml-2 shrink-0 rounded-full bg-zinc-100 px-2 py-0.5 text-xs text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">
                    v{skill.version}
                  </span>
                </div>
                <p className="mt-2 text-xs text-zinc-400">Installed {formatRelativeTime(skill.installed_at)}</p>
              </div>
            ))}
          </div>
        )}
      </div>

      <div>
        <h2 className="mb-3 text-sm font-semibold text-zinc-700 dark:text-zinc-300">Registry</h2>
        <form onSubmit={handleSearch} className="mb-3 flex gap-2">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-400" />
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search skills..."
              className="w-full rounded-lg border border-zinc-300 bg-white pl-9 pr-3 py-2 text-sm text-zinc-900 focus:border-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
            />
          </div>
          <button
            type="submit"
            disabled={searching}
            className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {searching ? '...' : 'Search'}
          </button>
        </form>

        {registry.length === 0 ? (
          <div className="rounded-lg border border-dashed border-zinc-300 bg-white py-8 text-center dark:border-zinc-700 dark:bg-zinc-900">
            <p className="text-sm text-zinc-400">No results</p>
          </div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {registry.map((skill) => (
              <div key={skill.name} className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
                <div className="flex items-start justify-between">
                  <div>
                    <p className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{skill.name}</p>
                    <p className="mt-0.5 text-xs text-zinc-500">{skill.description}</p>
                  </div>
                  <span className="ml-2 shrink-0 rounded-full bg-zinc-100 px-2 py-0.5 text-xs text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">
                    v{skill.version}
                  </span>
                </div>
                <p className="mt-2 text-xs text-zinc-400">by {skill.author}</p>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
