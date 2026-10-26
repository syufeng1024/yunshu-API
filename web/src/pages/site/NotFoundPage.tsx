import { Link } from 'react-router-dom'
import { buttonVariants } from '@/components/Button'
import { EmptyState } from '@/components/EmptyState'
import { ROUTES } from '@/lib/constants'
import { usePageTitle } from '@/lib/usePageTitle'

export default function NotFoundPage() {
  usePageTitle('页面不存在')

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col items-center px-4 py-20 sm:px-6">
      <EmptyState
        title="404 · 页面不存在"
        description="地址可能已变更，或该页面不在 V1.0 范围内。"
        icon={<span className="text-sm font-semibold text-ink-secondary">404</span>}
        action={
          <Link to={ROUTES.home} className={buttonVariants({ variant: 'primary', size: 'sm' })}>
            返回首页
          </Link>
        }
      />
    </div>
  )
}
