import { useEffect, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useRequestIds } from '@/api/console'
import type { RequestIDItem } from '@/api/console'
import { Badge, Button, EmptyState, ErrorState, Input } from '@/components'
import { logResultLabel } from '@/lib/consoleLabels'
import { formatDateTime, formatDateTimeMs } from '@/lib/format'
import { useDebouncedValue } from '@/lib/hooks/useDebouncedValue'

const PAGE_SIZE = 10

export interface RequestIDPickerProps {
  selected: RequestIDItem | null
  onSelect: (item: RequestIDItem) => void
  onClear: () => void
  /** False while the dialog is closed or a type that cannot link one is selected. */
  enabled: boolean
}

/**
 * The backend applies `user_id = ? AND created_at >= now()-90d` in SQL and
 * echoes the window start as `since`. This component must not send from/to or
 * filter rows locally (F19-1).
 */
export function RequestIDPicker({
  selected,
  onSelect,
  onClear,
  enabled,
}: RequestIDPickerProps) {
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const debouncedSearch = useDebouncedValue(search, 300)

  useEffect(() => {
    setPage(1)
  }, [debouncedSearch])

  const query = useRequestIds(
    { q: debouncedSearch.trim() || undefined, page, pageSize: PAGE_SIZE },
    enabled && selected === null,
  )

  if (selected) {
    return (
      <div className="space-y-2 rounded-lg border border-border bg-surface-muted px-3 py-2">
        <p className="text-xs text-ink-secondary">已选择关联请求</p>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="min-w-0 space-y-0.5">
            <p className="break-all font-mono text-xs text-ink">{selected.request_id}</p>
            <p className="text-xs text-ink-muted">
              {selected.model_name} · {formatDateTimeMs(selected.created_at)} · HTTP{' '}
              {selected.http_status}
            </p>
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              onClear()
              setSearch('')
              setPage(1)
            }}
          >
            清除关联请求
          </Button>
        </div>
      </div>
    )
  }

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const message = query.isError ? errorMessage(query.error) : null
  const hint = query.isError ? errorHint(query.error) : null

  return (
    <div className="space-y-2">
      <Input
        label="搜索可关联请求"
        type="search"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
        placeholder="输入 request_id 或模型名片段"
        hint="后端只返回本人最近 90 天的请求，不做本地时间过滤。"
        maxLength={64}
      />

      {query.data ? (
        <p className="text-xs text-ink-muted">
          可选范围自 {formatDateTime(query.data.since)} 起（90 天窗口，由后端计算）。
        </p>
      ) : null}

      {message ? (
        <ErrorState
          compact
          message={message}
          hint={hint}
          onRetry={() => void query.refetch()}
        />
      ) : query.isLoading ? (
        <p className="rounded-lg border border-border px-3 py-3 text-xs text-ink-muted">
          正在加载可关联请求…
        </p>
      ) : items.length === 0 ? (
        <EmptyState
          compact
          title="没有可关联的请求"
          description="最近 90 天内没有匹配的本人请求；也可以不关联请求直接提交。"
        />
      ) : (
        <ul aria-label="可关联请求列表" className="max-h-56 space-y-1 overflow-y-auto">
          {items.map((item) => (
            <li key={item.request_id}>
              <button
                type="button"
                aria-label={`选择请求 ${item.request_id}`}
                onClick={() => onSelect(item)}
                className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2 text-left transition-colors duration-fast hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              >
                <span className="break-all font-mono text-xs text-ink">
                  {item.request_id}
                </span>
                <span className="text-xs text-ink-secondary">{item.model_name}</span>
                <span className="text-xs text-ink-muted">
                  {formatDateTimeMs(item.created_at)}
                </span>
                <Badge variant={item.result === 'SUCCESS' ? 'success' : 'danger'}>
                  {logResultLabel(item.result)}
                </Badge>
                <span className="text-xs tabular-nums text-ink-muted">
                  HTTP {item.http_status}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}

      {total > PAGE_SIZE ? (
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs text-ink-secondary">
            第 {page} / {pageCount} 页 · 共 {total} 条
          </p>
          <div className="flex gap-2">
            <Button
              variant="secondary"
              size="sm"
              disabled={query.isFetching || page <= 1}
              onClick={() => setPage((current) => Math.max(1, current - 1))}
            >
              上一页
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={query.isFetching || page >= pageCount}
              onClick={() => setPage((current) => current + 1)}
            >
              下一页
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}
