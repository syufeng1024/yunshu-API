import { Input } from '@/components'

export type PriceDirection = 'raise' | 'cut'

const DATE_PAD = (value: number) => String(value).padStart(2, '0')

/** Local date-time value in the shape <input type="datetime-local"> expects. */
export function toLocalDateTimeValue(date: Date): string {
  return [
    `${date.getFullYear()}-${DATE_PAD(date.getMonth() + 1)}-${DATE_PAD(date.getDate())}`,
    `${DATE_PAD(date.getHours())}:${DATE_PAD(date.getMinutes())}`,
  ].join('T')
}

/**
 * Earliest allowed effective instant. A raise needs the seven-day public
 * notice window (pricing.go:19 / PRD F16-4); a cut may start immediately.
 * The backend judges the window against the server clock, so this is a
 * convenience only, never the authority.
 */
export function earliestEffectiveAt(direction: PriceDirection, now = new Date()): Date {
  const earliest = new Date(now.getTime())
  if (direction === 'raise') {
    earliest.setDate(earliest.getDate() + 7)
    // datetime-local only carries minutes. Round an exact now+7d up so the
    // chosen minute is never seconds short of the backend's threshold.
    if (earliest.getSeconds() > 0 || earliest.getMilliseconds() > 0) {
      earliest.setMinutes(earliest.getMinutes() + 1)
    }
  }
  earliest.setSeconds(0, 0)
  return earliest
}

export function isEffectiveTimeTooEarly(
  iso: string,
  direction: PriceDirection,
  now = new Date(),
): boolean {
  if (!iso) return false
  const picked = new Date(iso)
  if (Number.isNaN(picked.getTime())) return false
  return picked.getTime() < earliestEffectiveAt(direction, now).getTime()
}

export interface EffectiveTimePickerProps {
  direction: PriceDirection
  /** ISO 8601 string; an empty string means "not chosen yet". */
  value: string
  onChange: (iso: string) => void
  disabled?: boolean
  /** Injected clock for tests and deterministic previews. */
  now?: Date
}

export function EffectiveTimePicker({
  direction,
  value,
  onChange,
  disabled = false,
  now = new Date(),
}: EffectiveTimePickerProps) {
  const earliest = earliestEffectiveAt(direction, now)
  const minValue = toLocalDateTimeValue(earliest)
  const parsedValue = value ? new Date(value) : null
  const localValue =
    parsedValue && !Number.isNaN(parsedValue.getTime())
      ? toLocalDateTimeValue(parsedValue)
      : ''
  const tooEarly = isEffectiveTimeTooEarly(value, direction, now)

  const hint =
    direction === 'raise'
      ? `涨价需提前 7 天公示，最早可选 ${minValue.replace('T', ' ')}（本地时间）。未达公示期的保存会被后端拒绝。`
      : '降价可即时生效；选择未来时间则按所选时间生效。'

  return (
    <div data-testid="effective-time-picker">
      <Input
        label="生效时间"
        type="datetime-local"
        required
        data-testid="effective-time-input"
        min={minValue}
        value={localValue}
        disabled={disabled}
        error={tooEarly ? '所选时间早于最早可生效时间（涨价需提前 7 天公示）。' : undefined}
        hint={hint}
        onChange={(event) => {
          const next = event.target.value
          if (!next) {
            onChange('')
            return
          }
          const parsed = new Date(next)
          onChange(Number.isNaN(parsed.getTime()) ? '' : parsed.toISOString())
        }}
      />
    </div>
  )
}
