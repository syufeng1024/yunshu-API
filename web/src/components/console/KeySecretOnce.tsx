import { useState } from 'react'
import { cn } from '@/lib/cn'
import { writeClipboard } from '@/lib/clipboard'
import { Button } from '../Button'
import { Icon } from '../Icon'
import { Modal } from '../Modal'
import { useToast } from '../Toast'

export interface RevealedKey {
  id: number
  key_name: string
  /** Plaintext from the create/rotate response. Never persisted by this component. */
  key: string
}

export interface KeySecretOnceProps {
  open: boolean
  keys: readonly RevealedKey[]
  warning?: string
  title?: string
  /**
   * The parent clears its copy of the plaintext here. Once this fires the
   * secret is gone from React state and cannot be re-opened (F6-3).
   */
  onClose: () => void
}

/**
 * The single place a plaintext API key is shown. It is a controlled component:
 * the plaintext lives in the caller's state only while the dialog is open, and
 * closing drops it. Nothing is written to storage, the URL or the DOM outside
 * this dialog.
 */
export function KeySecretOnce({
  open,
  keys,
  warning,
  title = '密钥已生成',
  onClose,
}: KeySecretOnceProps) {
  const toast = useToast()
  const [copiedId, setCopiedId] = useState<number | null>(null)

  if (!open || keys.length === 0) return null

  const all = keys.map((item) => item.key).join('\n')

  const copyOne = async (item: RevealedKey) => {
    try {
      await writeClipboard(item.key)
      setCopiedId(item.id)
      toast.success('已复制密钥')
    } catch {
      toast.error('复制失败', '浏览器未授予剪贴板权限，请手动选择文本复制。')
    }
  }

  const copyAll = async () => {
    try {
      await writeClipboard(all)
      toast.success(`已复制全部 ${keys.length} 个密钥`)
    } catch {
      toast.error('复制失败', '浏览器未授予剪贴板权限，请手动选择文本复制。')
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description="完整密钥只在此处展示一次，关闭后无法再次查看。"
      size="md"
      footer={
        <>
          {keys.length > 1 ? (
            <Button variant="secondary" onClick={copyAll}>
              复制全部
            </Button>
          ) : null}
          <Button onClick={onClose}>我已保存，关闭</Button>
        </>
      }
    >
      <div className="space-y-4">
        <div
          role="alert"
          className="rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning"
        >
          {warning ?? '完整密钥仅本次返回，关闭后无法再次查看。请立即复制并妥善保存。'}
        </div>

        <ul className="space-y-3">
          {keys.map((item) => (
            <li key={item.id} className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <p className="truncate text-xs font-medium text-ink-secondary">{item.key_name}</p>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => void copyOne(item)}
                  leadingIcon={<Icon name={copiedId === item.id ? 'check' : 'copy'} className="h-3.5 w-3.5" />}
                >
                  {copiedId === item.id ? '已复制' : '复制'}
                </Button>
              </div>
              <code
                data-testid={`revealed-key-${item.id}`}
                // Long keys wrap instead of overflowing the modal.
                className={cn(
                  'block break-all rounded-lg border border-border-strong bg-surface-muted px-3 py-2',
                  'font-mono text-xs leading-relaxed text-ink',
                )}
              >
                {item.key}
              </code>
            </li>
          ))}
        </ul>

        <p className="text-xs text-ink-muted">
          关闭后列表与详情只显示前缀和掩码（例如 <code className="font-mono">sk-xxxx****xxxx</code>）。
        </p>
      </div>
    </Modal>
  )
}
