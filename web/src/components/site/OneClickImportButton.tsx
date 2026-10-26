import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  fetchPublicIntegrationLinks,
  requestImportCredentials,
} from '@/api/public'
import { Button } from '@/components'
import { executeOneClickImport } from '@/lib/deepLink'
import type { OneClickImportDeps } from '@/lib/deepLink'
import { ROUTES } from '@/lib/constants'

const defaultDeps: OneClickImportDeps = {
  getLink: async (toolKey) => {
    const response = await fetchPublicIntegrationLinks()
    return response.data.find((link) => link.tool_key === toolKey)
  },
  getCredentials: (toolKey) => requestImportCredentials(toolKey),
  navigate: (url) => {
    if (typeof window === 'undefined') return
    window.location.assign(url)
  },
  // No analytics provider yet. If one is added, it may receive only tool_key.
  track: () => undefined,
}

export interface OneClickImportButtonProps {
  toolKey: string
  supported: boolean
  authenticated: boolean
  /** Test seam: the page always uses the backend-backed defaults. */
  deps?: OneClickImportDeps
}

/**
 * F4-2 / F4-4. The button never renders a key and never puts one in state:
 * credentials are fetched inside executeOneClickImport, written into the
 * backend template, and handed straight to the browser navigation.
 */
export function OneClickImportButton({
  toolKey,
  supported,
  authenticated,
  deps,
}: OneClickImportButtonProps) {
  const [importing, setImporting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!supported) {
    return <p className="text-xs text-ink-muted">该工具没有深链模板，请按上方步骤手动填写。</p>
  }

  if (!authenticated) {
    return (
      <div className="space-y-2">
        <Button variant="secondary" size="sm" disabled>
          一键导入
        </Button>
        <p className="text-xs text-ink-secondary">
          请先登录并创建 Key，再使用一键导入。
          <Link
            to={`${ROUTES.login}?next=${encodeURIComponent(ROUTES.integrations)}`}
            className="ml-1 font-medium text-primary hover:text-primary-hover"
          >
            去登录
          </Link>
        </p>
      </div>
    )
  }

  const handleImport = async () => {
    setImporting(true)
    setError(null)
    try {
      await executeOneClickImport(toolKey, deps ?? defaultDeps)
    } catch {
      setError('一键导入失败，请按上方步骤手动配置。')
    } finally {
      setImporting(false)
    }
  }

  return (
    <div className="space-y-2">
      <Button variant="primary" size="sm" loading={importing} onClick={handleImport}>
        一键导入
      </Button>
      <p className="text-xs text-ink-muted">
        仅在点击时读取 Key，拼接深链后立即跳转，不在页面保留。
      </p>
      {error ? (
        <p role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : null}
    </div>
  )
}
