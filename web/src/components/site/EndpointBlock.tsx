import type { PublicEndpoint } from '@/api/public'
import { Badge, CopyBlock, EmptyState, ErrorState, Skeleton } from '@/components'

export interface EndpointBlockProps {
  endpoints: readonly PublicEndpoint[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
}

/**
 * F1-4: without health-check data the block shows only lines and Base URLs.
 * It never invents an availability state or a percentage.
 */
export function EndpointBlock({ endpoints, loading = false, error = null, onRetry }: EndpointBlockProps) {
  if (loading) {
    return (
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-56 w-full" />
        <Skeleton className="h-56 w-full" />
      </div>
    )
  }

  if (error) {
    return <ErrorState message={error} onRetry={onRetry} />
  }

  if (endpoints.length === 0) {
    return <EmptyState title="暂无接入点配置" description="接入点地址由后台维护。" />
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-4 lg:grid-cols-2">
        {endpoints.map((endpoint) => (
          <article
            key={endpoint.id}
            data-testid="endpoint-card"
            className="flex flex-col gap-4 rounded-xl border border-border bg-surface p-5 shadow-card"
          >
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={endpoint.role === 'PRIMARY' ? 'primary' : 'neutral'}>
                {endpoint.role === 'PRIMARY' ? '主接入点' : '灾备线路'}
              </Badge>
              <h3 className="text-sm font-semibold text-ink">{endpoint.name}</h3>
              {endpoint.health_status ? (
                <Badge variant={endpoint.health_status === 'PASS' ? 'success' : 'danger'}>
                  {endpoint.health_status === 'PASS' ? '健康检查通过' : '健康检查异常'}
                </Badge>
              ) : null}
            </div>

            <p className="text-sm text-ink-secondary">
              {endpoint.line} · {endpoint.description}
            </p>

            <CopyBlock label="Base URL" code={endpoint.base_url} />

            {endpoint.status_page_url ? (
              <a
                href={endpoint.status_page_url}
                target="_blank"
                rel="noreferrer noopener"
                className="text-sm font-medium text-primary hover:text-primary-hover"
              >
                查看服务状态
              </a>
            ) : null}
          </article>
        ))}
      </div>

      <p className="text-xs text-ink-muted">
        V1.0 使用单接入点 + 一个灾备地址；灾备需在主接入点故障时手工切换。
      </p>
    </div>
  )
}
