import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useAuthStore } from '@/stores/auth'
import IntegrationsPage from './IntegrationsPage'

beforeEach(() => {
  useAuthStore.setState({ status: 'anonymous', user: null, error: null, hint: null })
})

afterEach(() => {
  cleanup()
})

describe('IntegrationsPage', () => {
  it('F4-1: renders exactly five tool cards', () => {
    render(
      <MemoryRouter>
        <IntegrationsPage />
      </MemoryRouter>,
    )
    expect(screen.getAllByTestId('tool-card')).toHaveLength(5)
  })

  it('F4-4: a supported tool is disabled with a login hint for anonymous visitors', async () => {
    const user = userEvent.setup()
    const { container } = render(
      <MemoryRouter>
        <IntegrationsPage />
      </MemoryRouter>,
    )

    await user.click(screen.getByRole('button', { name: /Cherry Studio/ }))
    expect(screen.getByRole('button', { name: '一键导入' })).toBeDisabled()
    expect(screen.getByText(/请先登录并创建 Key/)).toBeInTheDocument()
    expect(container.textContent).not.toMatch(/更多工具|即将支持/)
  })

  it('F4-5: renders the generic OpenAI SDK fallback', () => {
    render(
      <MemoryRouter>
        <IntegrationsPage />
      </MemoryRouter>,
    )
    expect(screen.getByRole('heading', { level: 2, name: '通用兜底' })).toBeInTheDocument()
  })
})
