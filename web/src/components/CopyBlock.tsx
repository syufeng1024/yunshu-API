import { useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/cn'
import { writeClipboard } from '@/lib/clipboard'
import { Icon } from './Icon'
import { useToast } from './Toast'

export interface CopyBlockProps {
  code: string
  language?: string
  label?: string
  className?: string
}

/**
 * Copy-to-clipboard block. Deliberately does not persist anything: PRD F4-2
 * forbids writing API keys to browser storage, URLs, data-* or the DOM ahead
 * of the click. Callers pass only the already-visible snippet.
 */
export function CopyBlock({ code, language, label, className }: CopyBlockProps) {
  const [copied, setCopied] = useState(false)
  const resetTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const toast = useToast()

  useEffect(() => {
    return () => {
      if (resetTimer.current) clearTimeout(resetTimer.current)
    }
  }, [])

  const handleCopy = async () => {
    try {
      await writeClipboard(code)
      setCopied(true)
      toast.success('已复制')
      if (resetTimer.current) clearTimeout(resetTimer.current)
      resetTimer.current = setTimeout(() => setCopied(false), 2_000)
    } catch {
      toast.error('复制失败', '浏览器未授予剪贴板权限，请手动选择文本复制。')
    }
  }

  return (
    <div
      className={cn('overflow-hidden rounded-lg border border-sidebar-border bg-sidebar-bg', className)}
    >
      <div className="flex items-center justify-between gap-3 border-b border-sidebar-border px-4 py-2">
        <span className="text-xs text-sidebar-muted">{label ?? language ?? '代码'}</span>
        <button
          type="button"
          onClick={handleCopy}
          className="inline-flex items-center gap-1 rounded-sm px-2 py-1 text-xs text-sidebar-muted transition-colors duration-fast hover:bg-sidebar-hover hover:text-sidebar-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          aria-label={copied ? '已复制' : '复制代码'}
        >
          <Icon name={copied ? 'check' : 'copy'} className="h-3.5 w-3.5" />
          {copied ? '已复制' : '复制'}
        </button>
      </div>
      <pre className="overflow-x-auto p-4 text-xs leading-relaxed text-sidebar-text">
        <code className="font-mono">{code}</code>
      </pre>
    </div>
  )
}
