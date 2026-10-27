import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useCreateQuotaApply } from '@/api/console'
import { creditsFromMicro, formatCreditAmount, microFromCredits } from '@/lib/format'
import { Button } from '../Button'
import { Modal } from '../Modal'
import { useToast } from '../Toast'

export interface RaiseQuotaDialogProps {
  open: boolean
  /** Current daily ceiling in micro-credits; null means unlimited. */
  currentQuotaMicro: number | null
  onClose: () => void
}

function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `quota-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/**
 * F5-1: apply for a higher daily quota. The request lands in the ops review
 * queue; nothing is granted here. Target quota is entered in credits and
 * converted with the shared formatter helper, not with an inline division.
 */
export function RaiseQuotaDialog({ open, currentQuotaMicro, onClose }: RaiseQuotaDialogProps) {
  const toast = useToast()
  const mutation = useCreateQuotaApply()
  const [target, setTarget] = useState('')
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [hint, setHint] = useState<string | null>(null)
  const [idempotencyKey, setIdempotencyKey] = useState('')

  useEffect(() => {
    if (!open) return
    setTarget('')
    setReason('')
    setError(null)
    setHint(null)
    setIdempotencyKey(newIdempotencyKey())
  }, [open])

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setError(null)
    setHint(null)

    const credits = Number(target)
    if (!Number.isFinite(credits) || credits <= 0) {
      setError('请输入目标日额度（正整数，单位：积分）。')
      return
    }
    if (currentQuotaMicro !== null && credits <= creditsFromMicro(currentQuotaMicro)) {
      setError('目标额度必须高于当前日额度。')
      return
    }
    const trimmedReason = reason.trim()
    if (!trimmedReason) {
      setError('请填写申请理由。')
      return
    }

    try {
      await mutation.mutateAsync({
        input: {
          target_quota_micro: microFromCredits(credits),
          apply_reason: trimmedReason,
        },
        idempotencyKey,
      })
      toast.success('提额申请已提交', '运营审核通过后生效，可在用量与账单页查看进度。')
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setHint(errorHint(err))
    }
  }

  return (
    <Modal
      open={open}
      onClose={mutation.isPending ? () => undefined : onClose}
      title="申请提额"
      description="提交后进入运营审核队列，审核通过后按有效期生效。"
      size="sm"
    >
      <form className="space-y-4" onSubmit={handleSubmit}>
        <div className="rounded-lg bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
          当前日额度：
          <span className="ml-1 font-medium text-ink">
            {currentQuotaMicro === null ? '无上限' : `${formatCreditAmount(currentQuotaMicro)} 积分`}
          </span>
        </div>

        <label className="block space-y-1.5">
          <span className="block text-sm font-medium text-ink">目标日额度（积分）</span>
          <input
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            value={target}
            onChange={(event) => setTarget(event.target.value)}
            className="h-10 w-full rounded-lg border border-border-strong bg-surface px-3 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            placeholder="输入目标额度"
            aria-label="目标日额度（积分）"
          />
        </label>

        <label className="block space-y-1.5">
          <span className="block text-sm font-medium text-ink">申请理由</span>
          <textarea
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            rows={3}
            maxLength={500}
            className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            placeholder="说明业务场景与预计用量"
            aria-label="申请理由"
          />
        </label>

        {error ? (
          <div role="alert" className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs">
            <p className="text-danger">{error}</p>
            {hint ? <p className="mt-0.5 text-ink-secondary">{hint}</p> : null}
          </div>
        ) : null}

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
            取消
          </Button>
          <Button type="submit" loading={mutation.isPending}>
            提交申请
          </Button>
        </div>
      </form>
    </Modal>
  )
}
