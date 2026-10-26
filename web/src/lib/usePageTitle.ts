import { useEffect } from 'react'
import { BRAND_NAME, DEFAULT_TITLE } from './constants'

/**
 * The brand segment stays "云枢 API" and is separated from the page name by
 * a middle dot, per PRD §1.4.1. Callers pass only the page-specific part.
 */
export function usePageTitle(pageTitle?: string): void {
  useEffect(() => {
    document.title = pageTitle ? `${pageTitle} · ${BRAND_NAME}` : DEFAULT_TITLE
  }, [pageTitle])
}
