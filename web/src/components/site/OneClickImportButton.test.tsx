import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { OneClickImportDeps } from '@/lib/deepLink'
import { OneClickImportButton } from './OneClickImportButton'

const SECRET = 'sk-import-test-secret'

function secureDeps(): OneClickImportDeps {
  return {
    getLink: async () => ({
      tool_key: 'cherry-studio',
      deep_link_template:
        'cherrystudio://providers/import?baseUrl={base_url}&apiKey={api_key}&model={model}',
      enabled: true,
    }),
    getCredentials: async () => ({
      api_key: SECRET,
      base_url: 'https://api.yunshu.dev/v1',
      model: 'qwen-max',
    }),
    navigate: vi.fn(),
    track: vi.fn(),
  }
}

describe('OneClickImportButton', () => {
  afterEach(() => {
    cleanup()
  })

  it('F4-4: is disabled for anonymous visitors and points to login', () => {
    render(
      <MemoryRouter>
        <OneClickImportButton toolKey="cherry-studio" supported authenticated={false} />
      </MemoryRouter>,
    )

    expect(screen.getByRole('button', { name: '一键导入' })).toBeDisabled()
    expect(screen.getByText(/请先登录并创建 Key/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '去登录' })).toHaveAttribute(
      'href',
      '/login?next=%2Fintegrations',
    )
  })

  it('F4-2: never leaks the key into the DOM, attributes, title, page URL or analytics', async () => {
    const user = userEvent.setup()
    const deps = secureDeps()
    const { container } = render(
      <MemoryRouter>
        <OneClickImportButton
          toolKey="cherry-studio"
          supported
          authenticated
          deps={deps}
        />
      </MemoryRouter>,
    )

    const button = screen.getByRole('button', { name: '一键导入' })
    expect(container.textContent).not.toContain(SECRET)

    await user.click(button)
    await waitFor(() => expect(deps.navigate).toHaveBeenCalledTimes(1))

    const deepLink = vi.mocked(deps.navigate).mock.calls[0]?.[0] ?? ''
    expect(deepLink).toContain(encodeURIComponent(SECRET))

    expect(container.textContent).not.toContain(SECRET)
    expect(document.body.innerHTML).not.toContain(SECRET)
    expect(document.title).not.toContain(SECRET)
    expect(window.location.href).not.toContain(SECRET)

    const attributeValues = Array.from(container.querySelectorAll('*'))
      .flatMap((element) => Array.from(element.attributes).map((attribute) => attribute.value))
      .join(' ')
    expect(attributeValues).not.toContain(SECRET)

    const track = vi.mocked(deps.track!)
    expect(track).toHaveBeenCalledWith('integration_import', { tool_key: 'cherry-studio' })
    expect(JSON.stringify(track.mock.calls)).not.toContain(SECRET)
  })

  it('turns a missing backend template into a manual-config hint without a key', async () => {
    const user = userEvent.setup()
    const deps = secureDeps()
    deps.getLink = async () => undefined
    render(
      <MemoryRouter>
        <OneClickImportButton toolKey="cherry-studio" supported authenticated deps={deps} />
      </MemoryRouter>,
    )

    await user.click(screen.getByRole('button', { name: '一键导入' }))
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('请按上方步骤手动配置'),
    )
    expect(document.body.innerHTML).not.toContain(SECRET)
  })
})
