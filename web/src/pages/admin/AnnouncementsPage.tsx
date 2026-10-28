import { useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useAnnouncements,
  useCreateAnnouncement,
  usePublishAnnouncement,
  useUpdateAnnouncement,
} from '@/api/admin'
import type {
  Announcement,
  AnnouncementState,
  AnnouncementType,
  AnnouncementWriteInput,
} from '@/api/admin'
import {
  Badge,
  Button,
  Drawer,
  Input,
  PageHeader,
  Select,
  Table,
  Tabs,
  useToast,
} from '@/components'
import type { TableColumn } from '@/components'
import { toLocalDateTimeValue } from '@/components/admin'
import { AuditTrail } from '@/components/admin'
import { ConfirmDialog } from '@/components/console'
import { formatDateTime } from '@/lib/format'
import { usePageTitle } from '@/lib/usePageTitle'

const PAGE_SIZE = 20
const PRICE_NOTICE_WINDOW_MS = 7 * 24 * 60 * 60 * 1_000

type StateFilter = 'ALL' | AnnouncementState
type TypeFilter = 'ALL' | AnnouncementType
type PinnedFilter = 'ALL' | '0' | '1'

const STATE_LABEL: Record<AnnouncementState, string> = {
  DRAFT: '草稿',
  SCHEDULED: '已定时',
  PUBLISHED: '已发布',
  OFFLINE: '已下线',
}

const TYPE_LABEL: Record<AnnouncementType, string> = {
  NOTICE: '通知',
  MAINTENANCE: '维护',
  PROMOTION: '活动',
  PRICE_CHANGE: '调价',
}

const STATE_OPTIONS = [
  { value: 'ALL', label: '全部状态' },
  { value: 'DRAFT', label: STATE_LABEL.DRAFT },
  { value: 'SCHEDULED', label: STATE_LABEL.SCHEDULED },
  { value: 'PUBLISHED', label: STATE_LABEL.PUBLISHED },
  { value: 'OFFLINE', label: STATE_LABEL.OFFLINE },
]

const TYPE_OPTIONS = [
  { value: 'ALL', label: '全部类型' },
  { value: 'NOTICE', label: TYPE_LABEL.NOTICE },
  { value: 'MAINTENANCE', label: TYPE_LABEL.MAINTENANCE },
  { value: 'PROMOTION', label: TYPE_LABEL.PROMOTION },
  { value: 'PRICE_CHANGE', label: TYPE_LABEL.PRICE_CHANGE },
]

const EDITABLE_STATES: AnnouncementState[] = ['DRAFT', 'SCHEDULED']

interface AnnouncementFormState {
  title: string
  content: string
  annType: AnnouncementType
  positionMask: string
  pinned: boolean
  publishAt: string
  effectiveTo: string
}

function emptyForm(): AnnouncementFormState {
  return {
    title: '',
    content: '',
    annType: 'NOTICE',
    positionMask: '1',
    pinned: false,
    publishAt: '',
    effectiveTo: '',
  }
}

function formFromAnnouncement(announcement: Announcement): AnnouncementFormState {
  return {
    title: announcement.title,
    content: announcement.content,
    annType: announcement.ann_type,
    positionMask: String(announcement.position_mask),
    pinned: announcement.pinned,
    // Leave the schedule blank: an empty publish_at keeps the current
    // DRAFT/SCHEDULED state, while prefilling the past effective_from would
    // publish the notice immediately on save.
    publishAt: '',
    effectiveTo: announcement.effective_to ?? '',
  }
}

function parseInteger(raw: string): number | null {
  const normalized = raw
    .replace(/[０-９]/g, (char) => String.fromCharCode(char.charCodeAt(0) - 0xfee0))
    .replace(/[，,\s]/g, '')
  if (!/^\d+$/.test(normalized)) return null
  const value = Number(normalized)
  return Number.isSafeInteger(value) ? value : null
}

function validateAnnouncement(
  form: AnnouncementFormState,
  editing: Announcement | null,
): { errors: Record<string, string>; payload: AnnouncementWriteInput | null } {
  const errors: Record<string, string> = {}
  const title = form.title.trim()
  if (!title) errors.title = '请填写公告标题。'
  else if ([...title].length > 128) errors.title = '标题最多 128 个字符。'

  const content = form.content.trim()
  if (!content) errors.content = '请填写公告内容。'
  else if ([...content].length > 20_000) errors.content = '内容最多 20000 个字符。'

  const positionMask = parseInteger(form.positionMask)
  if (positionMask === null || positionMask < 1 || positionMask > 15) {
    errors.positionMask = '位置掩码需为 1–15 的整数。'
  }

  const now = new Date()
  let publishAt: Date | null = null
  if (form.publishAt) {
    publishAt = new Date(form.publishAt)
    if (Number.isNaN(publishAt.getTime())) {
      errors.publishAt = '定时发布时间格式不正确。'
    } else if (publishAt.getTime() <= now.getTime()) {
      errors.publishAt = '定时发布时间必须晚于当前时间；如需立即发布请使用「发布」按钮。'
    }
  }

  let effectiveTo: Date | null = null
  if (form.effectiveTo) {
    effectiveTo = new Date(form.effectiveTo)
    if (Number.isNaN(effectiveTo.getTime())) {
      errors.effectiveTo = '失效时间格式不正确。'
    }
  }

  const effectiveFrom = publishAt ?? (editing ? new Date(editing.effective_from) : now)
  if (
    effectiveTo &&
    !Number.isNaN(effectiveFrom.getTime()) &&
    effectiveTo.getTime() <= effectiveFrom.getTime()
  ) {
    errors.effectiveTo = '失效时间必须晚于公告生效时间。'
  }

  if (Object.keys(errors).length > 0) {
    return { errors, payload: null }
  }

  const payload: AnnouncementWriteInput = {
    title,
    content,
    ann_type: form.annType,
    position_mask: positionMask as number,
    pinned: form.pinned,
  }
  if (publishAt) payload.publish_at = publishAt.toISOString()
  if (effectiveTo) payload.effective_to = effectiveTo.toISOString()
  return { errors, payload }
}

function CheckboxField({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-ink">
      <input
        type="checkbox"
        className="h-4 w-4 rounded border-border-strong text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      {label}
    </label>
  )
}

export default function AdminAnnouncementsPage() {
  usePageTitle('公告与风控')
  const toast = useToast()

  const [stateFilter, setStateFilter] = useState<StateFilter>('ALL')
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('ALL')
  const [pinnedFilter, setPinnedFilter] = useState<PinnedFilter>('ALL')
  const [page, setPage] = useState(1)

  const drafts = useAnnouncements({
    state: 'DRAFT',
    page: 1,
    pageSize: PAGE_SIZE,
    sort: 'created_at',
    order: 'desc',
  })
  const list = useAnnouncements({
    page,
    pageSize: PAGE_SIZE,
    state: stateFilter === 'ALL' ? undefined : stateFilter,
    annType: typeFilter === 'ALL' ? undefined : typeFilter,
    pinned: pinnedFilter === 'ALL' ? undefined : pinnedFilter,
    sort: 'created_at',
    order: 'desc',
  })
  // Coarse promotion-vs-price-change hint. The announcement API has no
  // model_ids on reads, so this can only report a time-window overlap.
  const hintFeed = useAnnouncements({ page: 1, pageSize: 100 })

  const createAnnouncement = useCreateAnnouncement()
  const updateAnnouncement = useUpdateAnnouncement()
  const publishAnnouncement = usePublishAnnouncement()

  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Announcement | null>(null)
  const drawerOpen = creating || editing !== null
  const [form, setForm] = useState<AnnouncementFormState>(emptyForm)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saveHint, setSaveHint] = useState<string | null>(null)

  const [publishTarget, setPublishTarget] = useState<Announcement | null>(null)
  const [publishError, setPublishError] = useState<string | null>(null)

  const [auditOpen, setAuditOpen] = useState(false)
  const [auditTarget, setAuditTarget] = useState<{ type?: string; id?: string }>({})
  const [auditRefreshKey, setAuditRefreshKey] = useState(0)

  const openAudit = (targetType?: string, targetId?: number | string) => {
    setAuditTarget({
      type: targetType,
      id: targetId === undefined ? undefined : String(targetId),
    })
    setAuditRefreshKey((value) => value + 1)
    setAuditOpen(true)
  }

  const resetDrawer = () => {
    setCreating(false)
    setEditing(null)
    setErrors({})
    setSaveError(null)
    setSaveHint(null)
  }

  const closeDrawer = () => {
    if (createAnnouncement.isPending || updateAnnouncement.isPending) return
    resetDrawer()
  }

  const openCreate = () => {
    setEditing(null)
    setForm(emptyForm())
    setErrors({})
    setSaveError(null)
    setSaveHint(null)
    setCreating(true)
  }

  const openEdit = (announcement: Announcement) => {
    if (!EDITABLE_STATES.includes(announcement.state)) return
    setCreating(false)
    setForm(formFromAnnouncement(announcement))
    setErrors({})
    setSaveError(null)
    setSaveHint(null)
    setEditing(announcement)
  }

  const updateField = <K extends keyof AnnouncementFormState>(
    key: K,
    value: AnnouncementFormState[K],
  ) => {
    setForm((current) => ({ ...current, [key]: value }))
    setErrors((current) => {
      if (!current[key]) return current
      const next = { ...current }
      delete next[key]
      return next
    })
  }

  const handleSave = async () => {
    if (!creating && !editing) return
    const result = validateAnnouncement(form, editing)
    setErrors(result.errors)
    setSaveError(null)
    setSaveHint(null)
    if (!result.payload) return
    try {
      const response = creating
        ? await createAnnouncement.mutateAsync(result.payload)
        : await updateAnnouncement.mutateAsync({
            id: editing?.id as number,
            input: result.payload,
          })
      const announcement = response.announcement
      if (announcement.state === 'SCHEDULED') {
        toast.success(
          '已定时发布',
          `将于 ${formatDateTime(announcement.effective_from)} 由后端定时任务发布。`,
        )
      } else if (announcement.state === 'PUBLISHED') {
        toast.success('公告已发布')
      } else {
        toast.success('草稿已保存')
      }
      resetDrawer()
      openAudit('t_announcement', announcement.id)
    } catch (error) {
      setSaveError(errorMessage(error))
      setSaveHint(errorHint(error))
    }
  }

  const handlePublish = async () => {
    if (!publishTarget) return
    setPublishError(null)
    try {
      await publishAnnouncement.mutateAsync({ id: publishTarget.id, input: {} })
      toast.success('公告已发布')
      openAudit('t_announcement', publishTarget.id)
      setPublishTarget(null)
    } catch (error) {
      const hint = errorHint(error)
      setPublishError(hint ? `${errorMessage(error)}（${hint}）` : errorMessage(error))
    }
  }

  const now = Date.now()
  const windowEnd = now + PRICE_NOTICE_WINDOW_MS
  const priceChangeNotices = (hintFeed.data?.items ?? []).filter((announcement) => {
    if (announcement.ann_type !== 'PRICE_CHANGE') return false
    const from = new Date(announcement.effective_from).getTime()
    const to = announcement.effective_to
      ? new Date(announcement.effective_to).getTime()
      : Number.POSITIVE_INFINITY
    if (Number.isNaN(from) || Number.isNaN(to)) return false
    return from < windowEnd && to > now
  })

  const columns: TableColumn<Announcement>[] = [
    {
      key: 'title',
      header: '标题',
      render: (announcement) => (
        <div className="space-y-0.5">
          <p className="font-medium text-ink">{announcement.title}</p>
          <p className="line-clamp-2 max-w-md text-xs text-ink-muted">{announcement.content}</p>
        </div>
      ),
    },
    {
      key: 'type',
      header: '类型',
      render: (announcement) => (
        <Badge variant="primary">{TYPE_LABEL[announcement.ann_type]}</Badge>
      ),
    },
    {
      key: 'position',
      header: '位置',
      render: (announcement) => (
        <span className="text-xs text-ink-secondary" title="1–15 位掩码">
          掩码 {announcement.position_mask}
        </span>
      ),
    },
    {
      key: 'pinned',
      header: '置顶',
      render: (announcement) =>
        announcement.pinned ? <Badge variant="warning">置顶</Badge> : <span className="text-xs text-ink-muted">否</span>,
    },
    {
      key: 'effective',
      header: '生效起止',
      render: (announcement) => (
        <div className="space-y-0.5 text-xs text-ink-secondary">
          <p>起：{formatDateTime(announcement.effective_from)}</p>
          <p>
            止：
            {announcement.effective_to
              ? formatDateTime(announcement.effective_to)
              : '长期'}
          </p>
        </div>
      ),
    },
    {
      key: 'state',
      header: '状态',
      render: (announcement) => (
        <div className="space-y-1">
          <Badge
            variant={
              announcement.state === 'PUBLISHED'
                ? 'success'
                : announcement.state === 'SCHEDULED'
                  ? 'info'
                  : announcement.state === 'OFFLINE'
                    ? 'neutral'
                    : 'warning'
            }
          >
            {STATE_LABEL[announcement.state]}
          </Badge>
          {announcement.state === 'SCHEDULED' ? (
            <p className="text-xs text-ink-secondary">
              已定时，将于 {formatDateTime(announcement.effective_from)} 发布
            </p>
          ) : null}
        </div>
      ),
    },
    {
      key: 'created_at',
      header: '创建时间',
      render: (announcement) => (
        <span className="text-xs text-ink-secondary">
          {formatDateTime(announcement.created_at)}
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      align: 'right',
      render: (announcement) => {
        const editable = EDITABLE_STATES.includes(announcement.state)
        const published = announcement.state === 'PUBLISHED'
        return (
          <div className="flex flex-wrap items-center justify-end gap-1">
            <Button
              variant="ghost"
              size="sm"
              disabled={!editable}
              title={
                editable
                  ? undefined
                  : '已发布或已下线内容不可编辑；后端会拒绝该 PATCH。'
              }
              onClick={() => openEdit(announcement)}
            >
              编辑
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={published}
              title={
                published ? '已发布公告无需重复发布；后端会返回 409。' : undefined
              }
              onClick={() => {
                setPublishError(null)
                setPublishTarget(announcement)
              }}
            >
              发布
            </Button>
            {!editable || published ? (
              <span className="w-full text-right text-xs text-ink-muted">
                {published ? '已发布不可编辑、无需重复发布' : '已下线内容不可编辑'}
              </span>
            ) : null}
          </div>
        )
      },
    },
  ]

  const listError = list.isError ? errorMessage(list.error) : null
  const listHint = list.isError ? errorHint(list.error) : null
  const draftsError = drafts.isError ? errorMessage(drafts.error) : null
  const draftsHint = drafts.isError ? errorHint(drafts.error) : null
  const saving = createAnnouncement.isPending || updateAnnouncement.isPending

  const draftsSection = (
    <div className="space-y-3">
      <p className="text-xs text-ink-secondary">
        模型上下架自动生成的公告草稿会出现在这里；发布前请复核标题与内容。
      </p>
      {draftsHint ? (
        <p role="status" className="rounded-lg border border-danger bg-danger-soft px-4 py-2 text-xs text-danger">
          {draftsHint}
        </p>
      ) : null}
      <Table
        ariaLabel="公告草稿区"
        columns={columns}
        rows={drafts.data?.items ?? []}
        rowKey={(announcement) => String(announcement.id)}
        loading={drafts.isLoading}
        error={draftsError}
        onRetry={() => void drafts.refetch()}
        emptyTitle="暂无公告草稿"
        emptyDescription="模型上架/下架会自动生成草稿；也可以新建公告后保存为草稿。"
      />
    </div>
  )

  const allSection = (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <Select
          label="状态"
          value={stateFilter}
          onChange={(event) => {
            setStateFilter(event.target.value as StateFilter)
            setPage(1)
          }}
          options={STATE_OPTIONS}
        />
        <Select
          label="公告类型"
          value={typeFilter}
          onChange={(event) => {
            setTypeFilter(event.target.value as TypeFilter)
            setPage(1)
          }}
          options={TYPE_OPTIONS}
        />
        <Select
          label="置顶"
          value={pinnedFilter}
          onChange={(event) => {
            setPinnedFilter(event.target.value as PinnedFilter)
            setPage(1)
          }}
          options={[
            { value: 'ALL', label: '全部' },
            { value: '1', label: '仅置顶' },
            { value: '0', label: '仅非置顶' },
          ]}
        />
      </div>
      {listHint ? (
        <p role="status" className="rounded-lg border border-danger bg-danger-soft px-4 py-2 text-xs text-danger">
          {listHint}
        </p>
      ) : null}
      <Table
        ariaLabel="公告列表"
        columns={columns}
        rows={list.data?.items ?? []}
        rowKey={(announcement) => String(announcement.id)}
        loading={list.isLoading}
        error={listError}
        onRetry={() => void list.refetch()}
        emptyTitle="暂无公告"
        emptyDescription={
          stateFilter !== 'ALL' || typeFilter !== 'ALL' || pinnedFilter !== 'ALL'
            ? '当前筛选条件没有匹配的公告。'
            : '新建公告后保存为草稿，或直接发布。'
        }
        pagination={
          list.data
            ? {
                page: list.data.page,
                pageSize: list.data.page_size,
                total: list.data.total,
                onPageChange: setPage,
              }
            : undefined
        }
      />
    </div>
  )

  return (
    <div className="space-y-5">
      <PageHeader
        title="公告与风控"
        description="公告发布、定时发布与模型上下架自动草稿；当前无撤回接口。"
        actions={
          <>
            <Button variant="secondary" onClick={() => openAudit()}>
              审计记录
            </Button>
            <Button onClick={openCreate}>新建公告</Button>
          </>
        }
      />

      <p className="rounded-lg border border-warning bg-warning-soft px-4 py-2 text-xs text-warning">
        当前无撤回接口，如需下线请联系后端补口（T24）。公告发布后不可在原处撤回或删除。
      </p>

      <Tabs
        ariaLabel="公告分区"
        items={[
          { id: 'drafts', label: '草稿区', content: draftsSection },
          { id: 'all', label: '全部公告', content: allSection },
        ]}
      />

      <Drawer
        open={drawerOpen}
        onClose={closeDrawer}
        title={creating ? '新建公告' : '编辑公告'}
        description="保存为草稿或设置未来发布时间；到点发布由后端 jobsd 完成，前端不做本地定时。"
        size="lg"
        footer={
          <>
            <Button variant="secondary" onClick={closeDrawer} disabled={saving}>
              取消
            </Button>
            <Button onClick={() => void handleSave()} loading={saving}>
              保存
            </Button>
          </>
        }
      >
        <div className="space-y-5">
          {saveError ? (
            <div role="alert" className="space-y-1 rounded-lg border border-danger bg-danger-soft px-3 py-2">
              <p className="text-xs text-danger">{saveError}</p>
              {saveHint ? <p className="text-xs text-ink-muted">{saveHint}</p> : null}
            </div>
          ) : null}

          <Input
            label="标题"
            required
            value={form.title}
            error={errors.title}
            onChange={(event) => updateField('title', event.target.value)}
          />

          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-ink" htmlFor="announcement-content">
              内容
              <span className="ml-0.5 text-danger" aria-hidden="true">
                *
              </span>
            </label>
            <textarea
              id="announcement-content"
              className="min-h-32 w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              value={form.content}
              onChange={(event) => updateField('content', event.target.value)}
            />
            {errors.content ? (
              <p className="text-xs text-danger">{errors.content}</p>
            ) : (
              <p className="text-xs text-ink-muted">最多 20000 个字符。</p>
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <Select
              label="公告类型"
              value={form.annType}
              onChange={(event) => updateField('annType', event.target.value as AnnouncementType)}
              options={[
                { value: 'NOTICE', label: TYPE_LABEL.NOTICE },
                { value: 'MAINTENANCE', label: TYPE_LABEL.MAINTENANCE },
                { value: 'PROMOTION', label: TYPE_LABEL.PROMOTION },
                { value: 'PRICE_CHANGE', label: TYPE_LABEL.PRICE_CHANGE },
              ]}
            />
            <Input
              label="位置掩码"
              inputMode="numeric"
              value={form.positionMask}
              error={errors.positionMask}
              hint="1–15 的位掩码，由后端公告位定义。"
              onChange={(event) => updateField('positionMask', event.target.value)}
            />
          </div>

          <CheckboxField
            label="置顶"
            checked={form.pinned}
            onChange={(checked) => updateField('pinned', checked)}
          />

          <div className="grid gap-4 sm:grid-cols-2">
            <Input
              label="定时发布时间（可选）"
              type="datetime-local"
              value={form.publishAt ? toLocalDateTimeValue(new Date(form.publishAt)) : ''}
              error={errors.publishAt}
              hint="留空则保存为草稿；未来时间由后端到点发布；前端不允许过去时间。"
              onChange={(event) => {
                const value = event.target.value
                updateField('publishAt', value ? new Date(value).toISOString() : '')
              }}
            />
            <Input
              label="失效时间（可选）"
              type="datetime-local"
              value={form.effectiveTo ? toLocalDateTimeValue(new Date(form.effectiveTo)) : ''}
              error={errors.effectiveTo}
              hint="留空表示长期有效；编辑态留空不会清除后端已有值。"
              onChange={(event) => {
                const value = event.target.value
                updateField('effectiveTo', value ? new Date(value).toISOString() : '')
              }}
            />
          </div>

          <div className="rounded-lg border border-border bg-surface-muted p-3 text-xs text-ink-secondary">
            <p>
              生效起：
              {editing ? formatDateTime(editing.effective_from) : '保存时由后端决定'}
            </p>
            <p className="mt-1 text-ink-muted">
              写入接口不接收 effective_from；新建时由 publish_at 或后端当前时间决定，编辑时保留原生效时间（除非设置新的 publish_at）。
            </p>
          </div>

          {form.annType === 'PROMOTION' ? (
            <div className="space-y-2 rounded-lg border border-warning bg-warning-soft p-3 text-xs">
              <p className="font-medium text-warning">活动与调价冲突（粗提示）</p>
              <p className="text-ink-secondary">
                判定依据：现有 PRICE_CHANGE 公告的生效区间与未来 7 天调价窗口重叠。公告读取接口不返回 model_ids，无法判定具体模型；最终以后端保存/发布校验为准。
              </p>
              {hintFeed.isError ? (
                <p className="text-danger">
                  同期调价公告检查失败：{errorMessage(hintFeed.error)}
                </p>
              ) : priceChangeNotices.length === 0 ? (
                <p className="text-ink-secondary">
                  未发现同期 PRICE_CHANGE 公告（粗提示，不保证无冲突）。
                </p>
              ) : (
                <ul className="space-y-1">
                  {priceChangeNotices.map((announcement) => (
                    <li key={announcement.id} className="text-ink-secondary">
                      公告#{announcement.id}「{announcement.title}」（
                      {formatDateTime(announcement.effective_from)} ~{' '}
                      {announcement.effective_to
                        ? formatDateTime(announcement.effective_to)
                        : '长期'}
                      ）
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ) : null}
        </div>
      </Drawer>

      <ConfirmDialog
        open={publishTarget !== null}
        title="发布公告"
        description={
          publishTarget
            ? `发布后「${publishTarget.title}」立即对用户可见且不可编辑；当前无撤回接口。确认发布？`
            : undefined
        }
        confirmLabel="确认发布"
        loading={publishAnnouncement.isPending}
        error={publishError}
        onConfirm={() => void handlePublish()}
        onCancel={() => {
          if (publishAnnouncement.isPending) return
          setPublishTarget(null)
          setPublishError(null)
        }}
      />

      <AuditTrail
        open={auditOpen}
        onClose={() => setAuditOpen(false)}
        module="ANNOUNCEMENT"
        targetType={auditTarget.type}
        targetId={auditTarget.id}
        refreshKey={auditRefreshKey}
      />
    </div>
  )
}
