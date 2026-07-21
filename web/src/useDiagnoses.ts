import { useEffect, useRef, useState } from 'react'
import type { Diagnosis } from './types'

const POLL_INTERVAL_MS = 5000

interface State {
  diagnoses: Diagnosis[]
  loading: boolean
  error: string | null
  lastFetchedAt: Date | null
}

// useDiagnoses polls /api/diagnoses on an interval and keeps the latest
// result — no full-page reload, no client-side router, just one endpoint
// re-fetched in place.
export function useDiagnoses(): State {
  const [state, setState] = useState<State>({
    diagnoses: [],
    loading: true,
    error: null,
    lastFetchedAt: null,
  })
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    let cancelled = false

    async function fetchOnce() {
      abortRef.current?.abort()
      const controller = new AbortController()
      abortRef.current = controller
      try {
        const res = await fetch('api/diagnoses', { signal: controller.signal })
        if (!res.ok) throw new Error(`server returned ${res.status}`)
        const data = (await res.json()) as Diagnosis[] | null
        if (cancelled) return
        setState({ diagnoses: data ?? [], loading: false, error: null, lastFetchedAt: new Date() })
      } catch (err) {
        if (cancelled || (err instanceof DOMException && err.name === 'AbortError')) return
        setState((prev) => ({ ...prev, loading: false, error: (err as Error).message }))
      }
    }

    fetchOnce()
    const id = setInterval(fetchOnce, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
      abortRef.current?.abort()
    }
  }, [])

  return state
}
