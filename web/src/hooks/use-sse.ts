'use client'
import { useEffect } from 'react'

export function useSSE(
  url: string,
  onMessage: (data: string) => void,
  enabled = true,
) {
  useEffect(() => {
    if (!enabled) return

    let es: EventSource | null = null
    let timeoutId: ReturnType<typeof setTimeout> | null = null

    function open() {
      es = new EventSource(url)
      es.onmessage = (e: MessageEvent<string>) => {
        onMessage(e.data)
      }
      es.onerror = () => {
        es?.close()
        es = null
        timeoutId = setTimeout(open, 3000)
      }
    }

    open()

    return () => {
      es?.close()
      if (timeoutId !== null) clearTimeout(timeoutId)
    }
    // onMessage intentionally excluded - captured via stable function reference
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, enabled])
}
