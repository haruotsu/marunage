'use client'
import { useState, useEffect, useRef } from 'react'
import { sendToWorkspace } from '@/lib/api'

interface LiveStreamProps {
  taskId: number
  active: boolean
}

export function LiveStream({ taskId, active }: LiveStreamProps) {
  const [lines, setLines] = useState<string[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!active) return

    const es = new EventSource(`/api/tasks/${taskId}/stream`)
    esRef.current = es

    es.onmessage = (e: MessageEvent<string>) => {
      setLines((prev) => [...prev, e.data])
    }

    es.onerror = () => {
      es.close()
    }

    return () => {
      es.close()
      esRef.current = null
    }
  }, [taskId, active])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lines])

  async function handleSend(e: React.FormEvent) {
    e.preventDefault()
    if (!input.trim()) return
    setSending(true)
    try {
      await sendToWorkspace(taskId, input)
      setInput('')
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col rounded-lg border border-zinc-200 bg-zinc-950 dark:border-zinc-800">
      <div className="flex items-center justify-between border-b border-zinc-800 px-3 py-2">
        <span className="text-xs font-medium text-zinc-400">Terminal Stream</span>
        {active && (
          <span className="flex items-center gap-1.5 text-xs text-green-400">
            <span className="h-1.5 w-1.5 rounded-full bg-green-400 animate-pulse" />
            Live
          </span>
        )}
      </div>
      <div className="h-80 overflow-y-auto p-3 font-mono text-xs text-zinc-300">
        {lines.length === 0 ? (
          <span className="text-zinc-600">No output yet...</span>
        ) : (
          lines.map((line, i) => (
            <div key={i} className="whitespace-pre-wrap break-all">
              {line}
            </div>
          ))
        )}
        <div ref={bottomRef} />
      </div>
      {active && (
        <form
          onSubmit={handleSend}
          className="flex gap-2 border-t border-zinc-800 p-2"
        >
          <input
            type="text"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="Send text to workspace..."
            className="flex-1 rounded bg-zinc-900 px-2 py-1 font-mono text-xs text-zinc-200 outline-none placeholder:text-zinc-600 focus:ring-1 focus:ring-zinc-700"
          />
          <button
            type="submit"
            disabled={sending}
            className="rounded bg-zinc-700 px-3 py-1 text-xs text-zinc-200 hover:bg-zinc-600 disabled:opacity-50"
          >
            Send
          </button>
        </form>
      )}
    </div>
  )
}
