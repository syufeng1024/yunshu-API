import { useCallback, useEffect, useRef, useState } from 'react'
import { errorMessage } from '@/api/client'

export interface RealtimeMetric<T> {
  /** Last successful value. null on first load and after any failure. */
  value: T | null
  /** Friendly message for the last failure, cleared by the next success. */
  error: string | null
  loading: boolean
  /** Forces an immediate fetch without resetting the interval. */
  refresh: () => void
}

export interface RealtimeMetricOptions<T> {
  /** Poll period; the console contract is 5–10 seconds. Defaults to 8s. */
  intervalMs?: number
  enabled?: boolean
  /**
   * Optional value from the initial page load so the first paint is not empty.
   * It is replaced by the first poll and dropped as soon as a poll fails.
   */
  initialValue?: T | null
}

/**
 * Fixed-interval poller for rate-limit style metrics (PRD F7-2).
 *
 * A failed poll clears the value to null instead of keeping the previous number
 * or substituting 0: the detail page renders "--" for null and the user can
 * tell "unavailable" from a real zero. Late responses from an older interval
 * are ignored via a sequence counter.
 */
export function useRealtimeMetric<T>(
  fetcher: () => Promise<T>,
  options: RealtimeMetricOptions<T> = {},
): RealtimeMetric<T> {
  const { intervalMs = 8_000, enabled = true, initialValue = null } = options
  const [value, setValue] = useState<T | null>(initialValue)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(enabled)

  // Keeping the fetcher in a ref means an inline arrow on every render does
  // not tear down and recreate the interval.
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher

  const sequence = useRef(0)
  const mounted = useRef(true)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  const run = useCallback(async () => {
    const current = ++sequence.current
    try {
      const next = await fetcherRef.current()
      if (!mounted.current || current !== sequence.current) return
      setValue(next)
      setError(null)
    } catch (err) {
      if (!mounted.current || current !== sequence.current) return
      setValue(null)
      setError(errorMessage(err))
    } finally {
      if (mounted.current && current === sequence.current) setLoading(false)
    }
  }, [])

  const refresh = useCallback(() => {
    void run()
  }, [run])

  useEffect(() => {
    mounted.current = true
    if (!enabled) {
      setLoading(false)
      return () => {
        mounted.current = false
      }
    }
    setLoading(true)
    void run()
    timer.current = setInterval(() => {
      void run()
    }, intervalMs)
    return () => {
      mounted.current = false
      if (timer.current) {
        clearInterval(timer.current)
        timer.current = null
      }
    }
  }, [enabled, intervalMs, run])

  return { value, error, loading, refresh }
}
