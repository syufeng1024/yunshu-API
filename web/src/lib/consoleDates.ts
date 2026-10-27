/**
 * Date-range helpers for the console usage controls. They produce the plain
 * YYYY-MM-DD / YYYY-MM strings the API accepts; the backend owns the CST day
 * boundary, so no timezone arithmetic happens here.
 */

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

/** Local calendar date as YYYY-MM-DD (toISOString would shift across UTC). */
export function isoDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

export function todayISO(): string {
  return isoDate(new Date())
}

/** `days` days ago, inclusive: daysAgoISO(6) is the first of a 7-day range. */
export function daysAgoISO(days: number): string {
  const date = new Date()
  date.setDate(date.getDate() - days)
  return isoDate(date)
}

/** Current local month as YYYY-MM (the bill endpoint's month parameter). */
export function currentMonthISO(): string {
  const now = new Date()
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}`
}
