import {
  CartesianGrid,
  Legend,
  Line,
  LineChart as RechartsLineChart,
  ResponsiveContainer,
  Tooltip as RechartsTooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { cn } from '@/lib/cn'
import { EmptyState } from './EmptyState'
import { ErrorState } from './ErrorState'
import { Skeleton } from './Skeleton'

export type ChartDatum = Record<string, string | number>

export interface ChartSeries {
  key: string
  name: string
  /** Any CSS colour token, e.g. var(--primary). */
  color?: string
}

export interface LineChartProps {
  data: readonly ChartDatum[]
  xKey: string
  series: readonly ChartSeries[]
  height?: number
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  emptyTitle?: string
  emptyDescription?: string
  ariaLabel: string
  showLegend?: boolean
  className?: string
}

const DEFAULT_COLORS = [
  'var(--primary)',
  'var(--info)',
  'var(--success)',
  'var(--warning)',
  'var(--danger)',
]

interface ChartTooltipEntry {
  name?: string
  value?: string | number
  color?: string
  dataKey?: string | number
}

interface ChartTooltipContentProps {
  active?: boolean
  payload?: ChartTooltipEntry[]
  label?: string | number
}

function ChartTooltipContent({ active, payload, label }: ChartTooltipContentProps) {
  if (!active || !payload || payload.length === 0) return null
  return (
    <div className="rounded-lg border border-border bg-surface px-3 py-2 text-xs shadow-pop">
      {label !== undefined ? <p className="mb-1 font-medium text-ink">{label}</p> : null}
      <ul className="space-y-1">
        {payload.map((entry, index) => (
          <li key={`${entry.dataKey ?? index}`} className="flex items-center gap-2 text-ink-secondary">
            <span
              className="h-2 w-2 rounded-full"
              style={{ backgroundColor: entry.color ?? 'var(--primary)' }}
              aria-hidden="true"
            />
            <span>{entry.name}</span>
            <span className="ml-auto font-medium text-ink">{entry.value}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function LineChart({
  data,
  xKey,
  series,
  height = 280,
  loading = false,
  error = null,
  onRetry,
  emptyTitle = '暂无趋势数据',
  emptyDescription,
  ariaLabel,
  showLegend,
  className,
}: LineChartProps) {
  const frameClass = cn('w-full', className)
  const style = { height }

  if (error) {
    return (
      <div className={frameClass} style={style}>
        <ErrorState message={error} onRetry={onRetry} className="h-full" />
      </div>
    )
  }

  if (loading) {
    return (
      <div className={frameClass} style={style} aria-busy="true">
        <Skeleton className="h-full w-full" />
      </div>
    )
  }

  if (data.length === 0) {
    return (
      <div className={frameClass} style={style}>
        <EmptyState title={emptyTitle} description={emptyDescription} className="h-full" />
      </div>
    )
  }

  return (
    <div className={frameClass} style={style} role="img" aria-label={ariaLabel}>
      <ResponsiveContainer width="100%" height="100%">
        <RechartsLineChart data={[...data]} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="var(--border)" strokeDasharray="3 3" vertical={false} />
          <XAxis
            dataKey={xKey}
            stroke="var(--text-3)"
            axisLine={{ stroke: 'var(--border)' }}
            tickLine={false}
            tick={{ fill: 'var(--text-3)', fontSize: 12 }}
          />
          <YAxis
            stroke="var(--text-3)"
            axisLine={false}
            tickLine={false}
            width={48}
            tick={{ fill: 'var(--text-3)', fontSize: 12 }}
          />
          <RechartsTooltip
            cursor={{ stroke: 'var(--border-strong)' }}
            content={<ChartTooltipContent />}
          />
          {(showLegend ?? series.length > 1) ? (
            <Legend wrapperStyle={{ fontSize: 12, color: 'var(--text-2)' }} />
          ) : null}
          {series.map((item, index) => (
            <Line
              key={item.key}
              type="monotone"
              dataKey={item.key}
              name={item.name}
              stroke={item.color ?? DEFAULT_COLORS[index % DEFAULT_COLORS.length]}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 4 }}
              isAnimationActive={false}
              connectNulls
            />
          ))}
        </RechartsLineChart>
      </ResponsiveContainer>
    </div>
  )
}
