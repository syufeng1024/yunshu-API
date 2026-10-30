import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { errorMessage } from '@/api/client'
import { usePublicModels } from '@/api/public'
import { Badge, ErrorState } from '@/components'
import { hasListedImageModel, selectModelsByType, sortModelsForDisplay } from '@/lib/site'
import { usePageTitle } from '@/lib/usePageTitle'
import { cn } from '@/lib/cn'
import { buildDocChapters } from './docs/DocsChapters'

export default function DocsPage() {
  usePageTitle('开发者文档')

  const modelsQuery = usePublicModels()
  const models = modelsQuery.data?.data ?? []
  const listedModels = sortModelsForDisplay(models)
  const exampleModel = listedModels.find((model) => model.model_type === 'TEXT')?.model_name ?? '<模型名>'
  const imageModel = selectModelsByType(models, 'IMAGE')[0]?.model_name ?? '<图像模型名>'
  const hasImage = hasListedImageModel(models)

  const chapters = useMemo(
    () => buildDocChapters({ exampleModel, imageModel }, hasImage),
    [exampleModel, hasImage, imageModel],
  )

  const [searchParams, setSearchParams] = useSearchParams()
  const requested = searchParams.get('chapter')
  const active = chapters.find((chapter) => chapter.id === requested) ?? chapters[0]

  const selectChapter = (id: string) => {
    setSearchParams({ chapter: id }, { replace: true })
    try {
      window.scrollTo({ top: 0, behavior: 'smooth' })
    } catch {
      // jsdom does not implement scrolling; chapter switching stays client-side.
    }
  }

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-12 sm:px-6">
      <header className="border-b border-border pb-8">
        <Badge variant="primary" dot>
          V1.0 · {chapters.length} 个章节
        </Badge>
        <h1 className="mt-4 text-3xl font-semibold tracking-tight text-ink sm:text-4xl">开发者文档</h1>
        <p className="mt-3 max-w-2xl text-sm leading-relaxed text-ink-secondary">
          左侧章节导航，右侧正文。章节切换在客户端完成，不触发整页刷新；代码块右上角可直接复制。
        </p>
        <div className="mt-5 rounded-xl border border-warning bg-warning-soft p-4">
          <p className="text-sm font-medium text-ink">示例为虚拟值</p>
          <p className="mt-1 text-xs leading-relaxed text-ink-secondary">
            本章示例中的域名、Key 与请求 ID 均为虚拟值，仅用于说明请求结构。实际值以控制台创建结果与
            /v1/models 返回为准。
          </p>
        </div>
      </header>

      {modelsQuery.isError ? (
        <div className="mt-6">
          <ErrorState
            compact
            message={errorMessage(modelsQuery.error)}
            hint="模型列表示例暂未加载；图像生成章节与示例模型名会使用占位符。"
            onRetry={() => {
              void modelsQuery.refetch()
            }}
          />
        </div>
      ) : null}

      <div className="mt-8 grid gap-8 lg:grid-cols-[16rem_minmax(0,1fr)]">
        <nav aria-label="文档章节" className="min-w-0 lg:sticky lg:top-24 lg:self-start">
          {/* G19: nav 是 grid 子项，自动最小尺寸会取 <ol> 的 min-content——12 个
              whitespace-nowrap 章节按钮横向排开约 1017px 宽，把整页视口撑破。
              min-w-0 让 nav 回落到列宽，<ol> 的 overflow-x-auto 才真正生效，
              手机端变成可横向滑动的章节条。 */}
          <ol className="flex min-w-0 gap-1 overflow-x-auto pb-2 lg:flex-col lg:overflow-visible lg:pb-0">
            {chapters.map((chapter, index) => {
              const selected = chapter.id === active.id
              return (
                <li key={chapter.id} className="shrink-0 lg:shrink">
                  <button
                    type="button"
                    aria-current={selected ? 'page' : undefined}
                    onClick={() => selectChapter(chapter.id)}
                    className={cn(
                      'flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                      selected
                        ? 'bg-primary-soft font-medium text-primary-strong'
                        : 'text-ink-secondary hover:bg-surface-muted hover:text-ink',
                    )}
                  >
                    <span className="font-mono text-xs text-ink-muted">
                      {String(index + 1).padStart(2, '0')}
                    </span>
                    {chapter.title}
                  </button>
                </li>
              )
            })}
          </ol>
        </nav>

        <article key={active.id} className="min-w-0">
          <div className="rounded-2xl border border-border bg-surface p-5 shadow-card sm:p-7">
            <h2 className="text-xl font-semibold tracking-tight text-ink">{active.title}</h2>
            <div className="mt-4">{active.content}</div>
          </div>
          <p className="mt-4 text-xs text-ink-muted">
            示例代码中的域名、Key 与请求 ID 均为虚拟值；章节切换不会触发整页刷新。
          </p>
        </article>
      </div>
    </div>
  )
}
