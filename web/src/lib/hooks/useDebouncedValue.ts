import { useEffect, useState } from 'react'

/**
 * Delays a rapidly changing value (search input) by `delayMs`. The console
 * search box uses 300ms per PRD §3.2; typing a name must not fire one request
 * per keystroke.
 */
export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value)

  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])

  return debounced
}
