import { CopyBlock } from '@/components'

export interface CodeBlockProps {
  code: string
  language?: string
  label?: string
  className?: string
}

/**
 * F3-1: the copy button is supplied by CopyBlock, which writes the code value
 * to the clipboard byte-for-byte and resets the "已复制" label after 2s.
 */
export function CodeBlock({ code, language, label, className }: CodeBlockProps) {
  return <CopyBlock code={code} language={language} label={label} className={className} />
}
