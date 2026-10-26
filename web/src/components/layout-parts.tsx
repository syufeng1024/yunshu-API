import { COPYRIGHT } from '@/lib/constants'

/** Footer copyright line. Kept separate so both site ends render identical copy. */
export function Copyright() {
  return <p className="text-xs text-ink-muted">{COPYRIGHT}</p>
}
