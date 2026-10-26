import type { PublicModel } from '@/api/public'
import { Badge, ErrorState, Skeleton } from '@/components'
import { formatPricePerK, formatTokens } from '@/lib/format'
import { MODEL_TYPE_LABELS, selectRecommendedModels } from '@/lib/site'
import { SectionHeader } from './SectionHeader'

export interface ModelRecommendBlockProps {
  models: readonly PublicModel[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
}

/**
 * F1-7: sorted by recommend_weight DESC, then published_at DESC. If every
 * weight is zero the whole section is absent rather than an empty shell.
 */
export function ModelRecommendBlock({
  models,
  loading = false,
  error = null,
  onRetry,
}: ModelRecommendBlockProps) {
  const recommended = selectRecommendedModels(models)

  if (!loading && !error && recommended.length === 0) return null

  return (
    <section data-section="recommendations" className="mx-auto w-full max-w-6xl px-4 py-12 sm:px-6">
      <SectionHeader
        eyebrow="RECOMMENDED"
        title="推荐模型"
        description="按运营推荐权重排序；权重为 0 的模型不进入本区块。"
      />

      {error ? (
        <ErrorState className="mt-6" message={error} onRetry={onRetry} />
      ) : loading ? (
        <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }, (_, index) => (
            <Skeleton key={index} className="h-40 w-full" />
          ))}
        </div>
      ) : (
        <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {recommended.map((model, index) => (
            <article
              key={model.id}
              className="flex flex-col gap-3 rounded-xl border border-border bg-surface p-5 shadow-card transition-colors duration-fast hover:border-primary-ring"
            >
              <div className="flex items-start justify-between gap-3">
                <span className="font-mono text-xs text-ink-muted">
                  {String(index + 1).padStart(2, '0')}
                </span>
                <Badge variant="primary">{MODEL_TYPE_LABELS[model.model_type]}</Badge>
              </div>
              <div className="min-w-0">
                <h3 className="truncate text-base font-semibold text-ink">{model.display_name}</h3>
                <p className="truncate font-mono text-xs text-ink-muted">{model.model_name}</p>
              </div>
              <dl className="mt-auto space-y-1 text-xs text-ink-secondary">
                <div className="flex items-center justify-between gap-3">
                  <dt>上下文</dt>
                  <dd className="tabular-nums text-ink">{formatTokens(model.context_window)}</dd>
                </div>
                <div className="flex items-center justify-between gap-3">
                  <dt>输入</dt>
                  <dd className="tabular-nums text-ink">
                    {formatPricePerK(model.input_price_micro_per_k)}
                  </dd>
                </div>
                <div className="flex items-center justify-between gap-3">
                  <dt>输出</dt>
                  <dd className="tabular-nums text-ink">
                    {formatPricePerK(model.output_price_micro_per_k)}
                  </dd>
                </div>
              </dl>
            </article>
          ))}
        </div>
      )}
    </section>
  )
}
