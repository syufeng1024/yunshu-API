import type { ReactNode } from 'react'
import { Select } from '@/components'
import type { Region } from '@/api/admin'

/** V1.0 is domestic-only; channel.go:957-974 rejects OVERSEAS server-side too. */
export const DOMESTIC_REGION: Region = 'DOMESTIC'
export const OVERSEAS_REGION: Region = 'OVERSEAS'

export const SOURCE_REGION_OPTIONS = [
  { value: DOMESTIC_REGION, label: '境内' },
  { value: OVERSEAS_REGION, label: '境外' },
]

export function isSourceRegionBlocked(region: Region | string): boolean {
  return region === OVERSEAS_REGION
}

export interface SourceRegionGuardProps {
  value: Region
  onChange: (region: Region) => void
  /** The noun in the refusal copy: 渠道 (default) or 模型. */
  noun?: string
  disabled?: boolean
  /**
   * Optional save-action slot. It receives `blocked` so the caller can disable
   * the button before any request is made; rendering it inside the guard keeps
   * the selector and the disabled state together.
   */
  renderActions?: (state: { blocked: boolean }) => ReactNode
}

/**
 * Source-region selector. Choosing 境外 is stopped in the browser with the
 * same reason the backend returns ("V1.0 仅支持境内…"), so the operator never
 * learns about the limit from a failed save.
 */
export function SourceRegionGuard({
  value,
  onChange,
  noun = '渠道',
  disabled = false,
  renderActions,
}: SourceRegionGuardProps) {
  const blocked = isSourceRegionBlocked(value)

  return (
    <div className="space-y-2" data-testid="source-region-guard">
      <Select
        label="来源地"
        required
        data-testid="source-region-select"
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value as Region)}
        options={SOURCE_REGION_OPTIONS}
        hint={`V1.0 仅支持境内${noun}，境外选项会被前端与后端同时拒绝。`}
      />
      {blocked ? (
        <p
          role="alert"
          data-testid="source-region-blocked"
          className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs text-danger"
        >
          V1.0 仅支持境内{noun}，来源地不能选择「境外」。请选择「境内」后再保存。
        </p>
      ) : null}
      {renderActions ? <div className="flex justify-end gap-2 pt-1">{renderActions({ blocked })}</div> : null}
    </div>
  )
}
