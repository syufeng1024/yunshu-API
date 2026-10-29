import { describe, expect, it } from 'vitest'
import { buildDeepLink } from './deepLink'

describe('buildDeepLink', () => {
  it('F4-3: uses whatever parameter names the backend template declares', () => {
    const url = buildDeepLink(
      'example://import?endpoint={base_url}&token={api_key}&m={model}',
      {
        base_url: 'https://api.yunshu.dev/v1',
        api_key: 'sk-xxxxxxxx',
        model: 'qwen-max',
      },
    )

    expect(url).toContain('endpoint=https%3A%2F%2Fapi.yunshu.dev%2Fv1')
    expect(url).toContain('token=sk-xxxxxxxx')
    expect(url).toContain('m=qwen-max')
  })

  it('F4-3: {key} is the backend-canonical placeholder and {api_key} remains an equivalent alias', () => {
    const values = {
      base_url: 'https://api.yunshu.dev/v1',
      api_key: 'sk-abc/def+ghi=',
      model: 'qwen-max',
    }
    const encodedKey = 'sk-abc%2Fdef%2Bghi%3D'

    const canonical = buildDeepLink(
      'example://import?endpoint={base_url}&token={key}&m={model}',
      values,
    )
    expect(canonical).toContain(`token=${encodedKey}`)
    expect(canonical).not.toContain('{key}')
    expect(canonical).toContain('endpoint=https%3A%2F%2Fapi.yunshu.dev%2Fv1')
    expect(canonical).toContain('m=qwen-max')

    const legacy = buildDeepLink('example://import?token={api_key}', values)
    expect(legacy).toContain(`token=${encodedKey}`)
    expect(legacy).not.toContain('{api_key}')
  })
})
