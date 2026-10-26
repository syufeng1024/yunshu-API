import type { ImportCredentials, PublicIntegrationLink } from '@/api/public'

/**
 * F4-3: the protocol prefix, parameter names and structure all live in the
 * backend template. This function only substitutes values into whatever
 * placeholders the template declares.
 */
export function buildDeepLink(template: string, values: ImportCredentials): string {
  const map: Record<string, string> = {
    base_url: values.base_url,
    api_key: values.api_key,
    model: values.model,
  }
  return template.replace(/\{(base_url|api_key|model)\}/g, (match, key: string) => {
    const value = map[key]
    return value === undefined ? match : encodeURIComponent(value)
  })
}

export interface OneClickImportDeps {
  getLink: (toolKey: string) => Promise<PublicIntegrationLink | undefined>
  getCredentials: (toolKey: string) => Promise<ImportCredentials>
  navigate: (url: string) => void
  /** Analytics may only receive the tool key, never the key or deep link. */
  track?: (event: string, props: Record<string, string>) => void
}

/**
 * F4-2: credentials are fetched at click time, written into a local URL and
 * passed to the browser. Nothing is returned to the caller, so the key cannot
 * end up in React state, the DOM, browser storage or an analytics payload.
 */
export async function executeOneClickImport(
  toolKey: string,
  deps: OneClickImportDeps,
): Promise<void> {
  const link = await deps.getLink(toolKey)
  if (!link?.enabled || !link.deep_link_template) {
    throw new Error('deep_link_template_missing')
  }

  const credentials = await deps.getCredentials(toolKey)
  const url = buildDeepLink(link.deep_link_template, credentials)

  deps.track?.('integration_import', { tool_key: toolKey })
  deps.navigate(url)
}
