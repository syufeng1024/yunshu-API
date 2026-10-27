import type { ReactNode } from 'react'
import { Button } from '../Button'
import { ErrorState } from '../ErrorState'
import { Modal } from '../Modal'

export interface ConfirmDialogProps {
  open: boolean
  title: string
  description?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  variant?: 'primary' | 'danger'
  loading?: boolean
  error?: string | null
  onConfirm: () => void
  /** Called for the cancel button, Escape and the overlay click. */
  onCancel: () => void
}

/**
 * Second confirmation for destructive or permission-changing actions (F6-4).
 * The caller performs the mutation only from onConfirm, so cancelling leaves
 * the server state untouched.
 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = '确认',
  cancelLabel = '取消',
  variant = 'danger',
  loading = false,
  error = null,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  if (!open) return null

  return (
    <Modal
      open={open}
      onClose={loading ? () => undefined : onCancel}
      title={title}
      description={typeof description === 'string' ? description : undefined}
      size="sm"
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={loading}>
            {cancelLabel}
          </Button>
          <Button variant={variant} onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {typeof description !== 'string' && description ? (
          <div className="text-sm text-ink-secondary">{description}</div>
        ) : null}
        {error ? <ErrorState compact message={error} /> : null}
      </div>
    </Modal>
  )
}
