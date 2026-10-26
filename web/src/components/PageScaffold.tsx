import type { ReactNode } from 'react'
import { usePageTitle } from '@/lib/usePageTitle'
import { Badge } from './Badge'
import { EmptyState } from './EmptyState'
import { PageHeader } from './PageHeader'

export type ScaffoldTask = 'T18' | 'T19' | 'T20'

export interface PageScaffoldProps {
  title: string
  description: string
  task: ScaffoldTask
  sections: readonly string[]
  actions?: ReactNode
}

/**
 * Shared empty shell for the 22 V1.0 pages. It deliberately renders no metric
 * values: every number on these pages must come from the API in T18–T20.
 */
export function PageScaffold({ title, description, task, sections, actions }: PageScaffoldProps) {
  usePageTitle(title)

  return (
    <div className="space-y-6">
      <PageHeader
        title={title}
        description={description}
        actions={
          <>
            <Badge variant="primary">页面骨架</Badge>
            {actions}
          </>
        }
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {sections.map((section, index) => (
          <div
            key={section}
            className="rounded-lg border border-dashed border-border-strong bg-surface p-4"
          >
            <p className="text-xs font-medium text-ink-muted">
              {String(index + 1).padStart(2, '0')}
            </p>
            <p className="mt-1 text-sm font-medium text-ink">{section}</p>
          </div>
        ))}
      </div>

      <div className="rounded-xl border border-border bg-surface p-6 shadow-card">
        <EmptyState
          title="页面骨架已就位"
          description={`信息架构来自 PRD；接口、交互与异常态在 ${task} 接入。`}
        />
      </div>

      <p className="text-xs text-ink-muted">
        TODO({task})：接入真实接口后渲染数据、加载态、空态与错误态。
      </p>
    </div>
  )
}
