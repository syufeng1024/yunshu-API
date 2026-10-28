import { useEffect, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useAuditLogs } from '@/api/admin'
import type { AuditLogItem } from '@/api/admin'
import { Badge, Button, Drawer, EmptyState, ErrorState, Input, Skeleton } from '@/components'
import { formatDateTime } from '@/lib/format'

const PAGE_SIZE = 50

function resultVariant(result: string): 'success' | 'danger' | 'neutral' {
  if (result === 'OK') return 'success'
  if (result === 'REJECTED' || result === 'FAILED') return 'danger'
  return 'neutral'
}

function JsonValue({ label, value }: { label: string; value: unknown }) {
  if (value === null || value === undefined) {
    return <p className="text-xs text-ink-muted">{label}：无记录</p>
  }
  return (
    <details className="rounded-md border border-border bg-canvas px-2 py-1">
      <summary className="cursor-pointer text-xs font-medium text-ink-secondary">{label}</summary>
      <pre className="mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-all text-xs leading-relaxed text-ink">
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  )
}

function AuditRow({ item }: { item: AuditLogItem }) {
  return (
    <li
      data-testid="audit-log-item"
      className="space-y-2 rounded-lg border border-border bg-surface p-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={resultVariant(item.result)}>{item.result}</Badge>
        <span className="text-sm font-medium text-ink">{item.action}</span>
        <span className="text-xs text-ink-muted">{item.module}</span>
        <span className="ml-auto text-xs text-ink-muted">{formatDateTime(item.created_at)}</span>
      </div>
      <p className="text-xs text-ink-secondary">
        操作人 #{item.operator_id}（{item.operator_role}）
        {item.operator_ip ? ` · IP ${item.operator_ip}` : ''}
        {item.target_type ? ` · 目标 ${item.target_type}#${item.target_id}` : ''}
      </p>
      {item.reason ? <p className="text-xs text-ink-secondary">原因：{item.reason}</p> : null}
      <div className="grid gap-2 md:grid-cols-2">
        <JsonValue label="变更前 before" value={item.before} />
        <JsonValue label="变更后 after" value={item.after} />
      </div>
    </li>
  )
}

export interface AuditTrailProps {
  open: boolean
  onClose: () => void
  /** CHANNEL / MODEL / PRICE / GROUP / ANNOUNCEMENT / USER / REVIEW / TICKET. */
  module: string
  targetType?: string
  targetId?: number | string
  /**
   * Bump this after a successful write. When the drawer is already open, the
   * change triggers a refetch so the operator can read the before/after row.
   */
  refreshKey?: number
}

/**
 * Read-only audit drawer. Ordering is fixed to newest-first by the backend
 * (order=asc is a 400). target_type / target_id are not backend filters, so
 * they are applied to the returned page here (see api/admin.ts).
 */
export function AuditTrail({
  open,
  onClose,
  module,
  targetType,
  targetId,
  refreshKey = 0,
}: AuditTrailProps) {
  const [page, setPage] = useState(1)
  const [action, setAction] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')

  const query = useAuditLogs(
    {
      module,
      page,
      pageSize: PAGE_SIZE,
      action: action.trim() || undefined,
      from: from || undefined,
      to: to || undefined,
      targetType,
      targetId,
    },
    open,
  )

  useEffect(() => {
    if (open) void query.refetch()
    // Only the write counter and the open transition force a refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, refreshKey])

  // T24: target_type / target_id are SQL predicates now; no client-side trim.
  const items = query.data?.items ?? []

  const error = query.isError ? errorMessage(query.error) : null
  const hint = query.isError ? errorHint(query.error) : null
  const pageCount = query.data ? Math.max(1, Math.ceil(query.data.total / PAGE_SIZE)) : 1
  const targetNote =
    targetType || targetId !== undefined
      ? '按 target_type / target_id 服务端过滤，写后最新记录可直接查到。'
      : null

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="审计记录"
      description={`${module} 模块，按时间倒序（order 固定 desc）`}
      size="xl"
    >
      <div className="space-y-4" data-testid="audit-trail">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Input
            label="动作筛选"
            data-testid="audit-action-filter"
            value={action}
            placeholder="如 UPDATE"
            onChange={(event) => {
              setAction(event.target.value)
              setPage(1)
            }}
          />
          <Input
            label="起始日期"
            type="date"
            data-testid="audit-from-filter"
            value={from}
            onChange={(event) => {
              setFrom(event.target.value)
              setPage(1)
            }}
          />
          <Input
            label="截止日期"
            type="date"
            data-testid="audit-to-filter"
            value={to}
            onChange={(event) => {
              setTo(event.target.value)
              setPage(1)
            }}
          />
          <div className="flex items-end gap-2">
            <Button variant="secondary" onClick={() => void query.refetch()}>
              刷新
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setAction('')
                setFrom('')
                setTo('')
                setPage(1)
              }}
            >
              重置
            </Button>
          </div>
        </div>

        {targetNote ? <p className="text-xs text-ink-muted">{targetNote}</p> : null}

        {error ? (
          <div data-testid="audit-trail-error">
            <ErrorState message={error} hint={hint} onRetry={() => void query.refetch()} compact />
          </div>
        ) : query.isLoading ? (
          <div className="space-y-2" data-testid="audit-trail-loading">
            {Array.from({ length: 3 }, (_, index) => (
              <Skeleton key={index} className="h-24 w-full" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <div data-testid="audit-trail-empty">
            <EmptyState
              compact
              title="暂无审计记录"
              description={
                targetNote
                  ? '当前页没有匹配该目标行的记录；可清除目标筛选或扩大时间范围。'
                  : '该模块在所选时间窗内没有写操作记录。'
              }
            />
          </div>
        ) : (
          <>
            <ul className="space-y-2" data-testid="audit-trail-list">
              {items.map((item) => (
                <AuditRow key={item.id} item={item} />
              ))}
            </ul>
            {query.data && query.data.total > PAGE_SIZE ? (
              <div className="flex items-center justify-between gap-3">
                <p className="text-xs text-ink-secondary">
                  第 {page} / {pageCount} 页 · 共 {query.data.total} 条
                </p>
                <div className="flex gap-2">
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={page <= 1}
                    onClick={() => setPage((value) => Math.max(1, value - 1))}
                  >
                    上一页
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={page >= pageCount}
                    onClick={() => setPage((value) => value + 1)}
                  >
                    下一页
                  </Button>
                </div>
              </div>
            ) : null}
          </>
        )}
      </div>
    </Drawer>
  )
}
