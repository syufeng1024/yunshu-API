import { useId, useMemo, useState } from 'react'
import type { ModelType, PublicModel } from '@/api/public'
import { Badge, Table } from '@/components'
import type { TableColumn } from '@/components'
import { formatPricePerK, formatTokens } from '@/lib/format'
import { cn } from '@/lib/cn'
import {
  MODEL_TYPE_LABELS,
  PROTOCOL_LABELS,
  selectModelsByType,
  sortModelsForDisplay,
} from '@/lib/site'

export interface PriceTableProps {
  models: readonly PublicModel[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  className?: string
}

const GROUPS: readonly ModelType[] = ['TEXT', 'IMAGE']

const columns: readonly TableColumn<PublicModel>[] = [
  {
    key: 'model',
    header: '模型',
    render: (model) => (
      <div className="min-w-0">
        <p className="font-medium text-ink">{model.display_name}</p>
        <p className="font-mono text-xs text-ink-muted">{model.model_name}</p>
      </div>
    ),
  },
  {
    key: 'type',
    header: '类型',
    render: (model) => <Badge variant="neutral">{MODEL_TYPE_LABELS[model.model_type]}</Badge>,
  },
  {
    key: 'context',
    header: '上下文',
    render: (model) => <span className="tabular-nums">{formatTokens(model.context_window)}</span>,
  },
  {
    key: 'input',
    header: '输入单价',
    render: (model) => <span className="tabular-nums">{formatPricePerK(model.input_price_micro_per_k)}</span>,
  },
  {
    key: 'output',
    header: '输出单价',
    render: (model) => <span className="tabular-nums">{formatPricePerK(model.output_price_micro_per_k)}</span>,
  },
  {
    key: 'protocols',
    header: '协议',
    render: (model) => (
      <span className="text-xs text-ink-secondary">
        {model.protocols.map((protocol) => PROTOCOL_LABELS[protocol]).join(' / ')}
      </span>
    ),
  },
]

/**
 * F1-1 / F1-6: one table with a text/image switch. Only LISTED models can
 * reach this component through selectModelsByType.
 */
export function PriceTable({ models, loading = false, error = null, onRetry, className }: PriceTableProps) {
  const prefix = useId()
  const [group, setGroup] = useState<ModelType>('TEXT')
  const rows = useMemo(
    () => sortModelsForDisplay(selectModelsByType(models, group)),
    [group, models],
  )

  return (
    <div className={className}>
      <div
        role="tablist"
        aria-label="模型分组"
        className="mb-4 inline-flex rounded-lg border border-border bg-surface p-1"
      >
        {GROUPS.map((item) => {
          const selected = item === group
          return (
            <button
              key={item}
              id={`${prefix}-tab-${item}`}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-controls={`${prefix}-panel-${item}`}
              onClick={() => setGroup(item)}
              className={cn(
                'rounded-md px-4 py-1.5 text-sm font-medium transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                selected
                  ? 'bg-primary text-ink-inverse'
                  : 'text-ink-secondary hover:bg-surface-muted hover:text-ink',
              )}
            >
              {MODEL_TYPE_LABELS[item]}
            </button>
          )
        })}
      </div>

      <div
        role="tabpanel"
        id={`${prefix}-panel-${group}`}
        aria-labelledby={`${prefix}-tab-${group}`}
      >
        <Table
          ariaLabel="模型价格表"
          columns={columns}
          rows={rows}
          rowKey={(model) => model.id}
          loading={loading}
          error={error}
          onRetry={onRetry}
          emptyTitle={`暂无已上架${MODEL_TYPE_LABELS[group]}模型`}
          emptyDescription={
            group === 'IMAGE'
              ? '当前没有已上架图像模型，图像分组暂不提供价格。'
              : '当前没有已上架文本模型。'
          }
        />
      </div>

      <p className="mt-3 text-xs text-ink-muted">以上为公开标准单价，单位为积分/千 token。</p>
    </div>
  )
}
