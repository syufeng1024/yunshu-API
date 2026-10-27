import { useMemo, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useBill,
  useConsoleModels,
  useUsage,
  type UsageDimension,
  type UsageParams,
} from '@/api/console'
import { Button, ErrorState, Input, LineChart, PageHeader, Select } from '@/components'
import { BillSummary, UsageDetailTable } from '@/components/console'
import { dimensionLabel } from '@/lib/consoleLabels'
import { currentMonthISO, daysAgoISO, todayISO } from '@/lib/consoleDates'
import {
  creditsFromMicro,
  formatBasisPoints,
  formatCreditAmount,
  formatDateTime,
  formatInteger,
} from '@/lib/format'
import { useUsageExport } from '@/hooks/useUsageExport'
import { usePageTitle } from '@/lib/usePageTitle'

const DIMENSIONS: UsageDimension[] = ['model', 'channel', 'day']

const DIMENSION_HINT: Record<UsageDimension, string> = {
  model: '按模型聚合，排序为消耗积分降序。',
  channel: '按渠道聚合，模型筛选保持不变。',
  day: '按 CST 自然日聚合，模型筛选保持不变。',
}

export default function UsagePage() {
  usePageTitle('用量与账单')
  const [dimension, setDimension] = useState<UsageDimension>('model')
  const [from, setFrom] = useState(() => daysAgoISO(6))
  const [to, setTo] = useState(() => todayISO())
  const [modelId, setModelId] = useState('')
  const [month, setMonth] = useState(() => currentMonthISO())

  // Dimension is the only thing a tab changes. From/to and model stay put, so
  // the backend regroups the exact same slice of rows (F9-1).
  const filters = useMemo<UsageParams>(
    () => ({
      from,
      to,
      dimension,
      modelId: modelId ? Number(modelId) : undefined,
    }),
    [from, to, dimension, modelId],
  )

  const usage = useUsage(filters)
  const bill = useBill(month)
  const models = useConsoleModels()
  const exporter = useUsageExport(filters)

  const usageError = usage.isError ? errorMessage(usage.error) : null
  const usageHint = usage.isError ? errorHint(usage.error) : null
  const billError = bill.isError ? errorMessage(bill.error) : null
  const billHint = bill.isError ? errorHint(bill.error) : null

  const modelOptions = useMemo(
    () => [
      { value: '', label: '全部模型' },
      ...(models.data ?? []).map((model) => ({
        value: String(model.id),
        label: model.display_name,
      })),
    ],
    [models.data],
  )

  const chartData = useMemo(() => {
    const items = usage.data?.items ?? []
    return items.map((row) => ({
      label: row.name || `#${row.id}`,
      requests: row.request_total,
      credits: creditsFromMicro(row.consumed_micro),
    }))
  }, [usage.data?.items])

  const startError = exporter.start.isError ? errorMessage(exporter.start.error) : null
  const startHint = exporter.start.isError ? errorHint(exporter.start.error) : null
  const statusError = exporter.status.isError ? errorMessage(exporter.status.error) : null
  const statusHint = exporter.status.isError ? errorHint(exporter.status.error) : null
  const exportStatus = exporter.status.data

  return (
    <div className="space-y-6">
      <PageHeader
        title="用量与账单"
        description="聚合与金额均由后端计算；切换维度保留时间范围与模型筛选。"
      />

      <section className="space-y-4 rounded-xl border border-border bg-surface p-5 shadow-card">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Input
            label="开始日期"
            type="date"
            value={from}
            max={to}
            onChange={(event) => setFrom(event.target.value)}
            hint="最长查询窗口为 365 天"
          />
          <Input
            label="结束日期"
            type="date"
            value={to}
            min={from}
            onChange={(event) => setTo(event.target.value)}
          />
          <Select
            label="模型筛选"
            value={modelId}
            onChange={(event) => setModelId(event.target.value)}
            options={modelOptions}
            hint="切换维度时该筛选不变"
          />
          <div className="space-y-1.5">
            <span className="block text-sm font-medium text-ink">聚合维度</span>
            <div className="flex flex-wrap gap-1" role="group" aria-label="聚合维度">
              {DIMENSIONS.map((option) => (
                <Button
                  key={option}
                  size="sm"
                  variant={dimension === option ? 'primary' : 'secondary'}
                  aria-pressed={dimension === option}
                  onClick={() => setDimension(option)}
                >
                  {dimensionLabel(option)}
                </Button>
              ))}
            </div>
            <p className="text-xs text-ink-muted">{DIMENSION_HINT[dimension]}</p>
          </div>
        </div>

        {usageError ? (
          <ErrorState
            message={usageError}
            hint={usageHint}
            onRetry={() => void usage.refetch()}
          />
        ) : (
          <div className="grid gap-3 text-sm sm:grid-cols-3 xl:grid-cols-5">
            <div>
              <p className="text-xs text-ink-secondary">请求数（含失败）</p>
              <p className="mt-1 font-medium tabular-nums text-ink">
                {usage.data ? formatInteger(usage.data.totals.request_total) : '—'}
              </p>
            </div>
            <div>
              <p className="text-xs text-ink-secondary">成功请求</p>
              <p className="mt-1 font-medium tabular-nums text-ink">
                {usage.data ? formatInteger(usage.data.totals.request_success) : '—'}
              </p>
            </div>
            <div>
              <p className="text-xs text-ink-secondary">成功率</p>
              <p className="mt-1 font-medium tabular-nums text-ink">
                {usage.data ? formatBasisPoints(usage.data.totals.success_rate_bp) : '—'}
              </p>
            </div>
            <div>
              <p className="text-xs text-ink-secondary">消耗积分</p>
              <p className="mt-1 font-medium tabular-nums text-ink">
                {usage.data ? `${formatCreditAmount(usage.data.totals.consumed_micro)} 积分` : '—'}
              </p>
            </div>
            <div>
              <p className="text-xs text-ink-secondary">金额</p>
              <p className="mt-1 font-medium tabular-nums text-ink">
                {usage.data ? `${usage.data.totals.amount_yuan} 元` : '—'}
              </p>
            </div>
          </div>
        )}
      </section>

      <section className="space-y-3 rounded-xl border border-border bg-surface p-5 shadow-card">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-ink">
              {dimension === 'day' ? '按日用量趋势' : '维度消耗对比'}
            </h2>
            <p className="text-xs text-ink-secondary">
              请求数与消耗积分均来自后端聚合结果，不在前端二次计算。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void exporter.begin()}
              loading={exporter.start.isPending}
            >
              导出 CSV
            </Button>
          </div>
        </div>

        <LineChart
          data={chartData}
          xKey="label"
          series={[
            { key: 'requests', name: '请求数' },
            { key: 'credits', name: '消耗积分' },
          ]}
          height={260}
          loading={usage.isLoading}
          error={usageError}
          onRetry={() => void usage.refetch()}
          ariaLabel="用量趋势图"
        />

        <div className="space-y-2 rounded-lg border border-border bg-surface-muted px-4 py-3 text-xs">
          {startError ? (
            <ErrorState compact message={startError} hint={startHint} />
          ) : statusError ? (
            <div className="space-y-2">
              <ErrorState compact message={statusError} hint={statusHint} />
              <Button size="sm" variant="secondary" onClick={exporter.reset}>
                重新发起导出
              </Button>
            </div>
          ) : exportStatus?.state === 'DONE' && exportStatus.url ? (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-ink-secondary">
                CSV 已生成，有效至 {formatDateTime(exportStatus.expires_at)}。
              </p>
              <a
                href={exportStatus.url}
                download
                className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-ink-inverse hover:bg-primary-hover"
              >
                下载 CSV
              </a>
            </div>
          ) : exportStatus?.state === 'FAILED' ? (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-danger">导出任务失败，请重新发起。</p>
              <Button size="sm" variant="secondary" onClick={exporter.reset}>
                重新发起导出
              </Button>
            </div>
          ) : exporter.taskId ? (
            <p role="status" className="text-ink-secondary">
              导出任务运行中，完成后自动出现下载入口。
            </p>
          ) : (
            <p className="text-ink-muted">
              导出使用当前时间范围、维度与模型筛选；单个账号同一时间只能运行一个导出任务。
            </p>
          )}
        </div>
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-ink">用量明细</h2>
            <p className="text-xs text-ink-secondary">
              列定义与 CSV 表头由后端同一下发，页面按返回顺序渲染。
            </p>
          </div>
        </div>
        <UsageDetailTable
          data={usage.data}
          loading={usage.isLoading}
          error={usageError}
          onRetry={() => void usage.refetch()}
        />
      </section>

      <section className="space-y-3 rounded-xl border border-border bg-surface p-5 shadow-card">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-ink">月度账单</h2>
            <p className="text-xs text-ink-secondary">
              按 CST 自然月统计；显示金额与积分双口径，二者均直接取自后端。
            </p>
          </div>
          <div className="w-40">
            <Input
              label="账单月份"
              type="month"
              value={month}
              onChange={(event) => setMonth(event.target.value)}
            />
          </div>
        </div>

        <BillSummary
          bill={bill.data}
          loading={bill.isLoading}
          error={billError}
          hint={billHint}
          onRetry={() => void bill.refetch()}
        />
      </section>
    </div>
  )
}
