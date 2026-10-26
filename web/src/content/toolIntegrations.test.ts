import { describe, expect, it } from 'vitest'
import { fixtureResponses } from '@/fixtures/public'
import { TOOL_INTEGRATIONS } from './toolIntegrations'

const TOOL_WHITELIST = ['Cursor', 'Claude Code', 'Cherry Studio', 'NextChat', 'OpenAI SDK']

describe('tool integration content source', () => {
  it('F4-1: contains exactly the five V1.0 tools', () => {
    expect(TOOL_INTEGRATIONS).toHaveLength(5)
    expect(TOOL_INTEGRATIONS.map((tool) => tool.name)).toEqual(TOOL_WHITELIST)
  })

  it('F4-3: every one-click tool has a backend-configured template', () => {
    const links = fixtureResponses['/api/public/integration-links'].data
    const enabledKeys = new Set(
      links.filter((link) => link.enabled && link.deep_link_template).map((link) => link.tool_key),
    )
    for (const tool of TOOL_INTEGRATIONS) {
      if (tool.oneClick) expect(enabledKeys.has(tool.key)).toBe(true)
    }
  })

  it('contains no banned marketing terms', () => {
    const text = JSON.stringify(TOOL_INTEGRATIONS)
    expect(text).not.toMatch(/代理|加盟|许可证|折|即将支持|更多工具|注册即送/)
  })
})
