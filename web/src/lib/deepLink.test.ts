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
})
