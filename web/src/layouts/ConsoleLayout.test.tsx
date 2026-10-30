import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CurrentUser } from '@/stores/auth'
import { useAuthStore } from '@/stores/auth'
import { AdminLayout } from './AdminLayout'
import { ConsoleLayout } from './ConsoleLayout'

/**
 * 控制台必须让用户随时看到自己的调用地址（网关 `/v1` 前缀），并能一键复制。
 *
 * 背景：改造前控制台任何页面都看不到调用地址，用户写代码只能靠猜或翻文档。
 * 地址一律由 `VITE_API_BASE_URL` 推导，未注入时回落到站点自身域名——因此断言
 * 期望值只能用 `window.location.origin` 计算，不能写死任何域名。
 */

const user: CurrentUser = {
  id: '1',
  email: 'demo@yunshu.test',
  display_name: '演示账号',
  role: 'SUPER_ADMIN',
  status: 'NORMAL',
  csrf_token: 'csrf',
}

function renderShell(element: React.ReactElement, path: string) {
  const router = createMemoryRouter([{ path: '/', element, children: [{ index: true, element: null }] }], {
    initialEntries: [path],
  })
  return render(<RouterProvider router={router} />)
}

function signIn() {
  act(() => {
    useAuthStore.setState({ status: 'authenticated', user })
  })
}

const writeText = vi.fn(() => Promise.resolve())

beforeEach(() => {
  writeText.mockClear()
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })
})

afterEach(() => {
  act(() => {
    useAuthStore.setState({ status: 'anonymous', user: null, error: null, hint: null })
  })
})

describe('控制台常驻调用地址', () => {
  it('控制台显示由站点域名推导的 /v1 地址', async () => {
    signIn()
    renderShell(<ConsoleLayout />, '/')

    expect(screen.getByText('调用地址')).toBeInTheDocument()
    expect(
      screen.getByText(`${window.location.origin}/v1`),
    ).toBeInTheDocument()
  })

  it('复制按钮把地址写入剪贴板并给出「已复制」反馈', async () => {
    signIn()
    renderShell(<ConsoleLayout />, '/')

    // fireEvent keeps the clipboard stub installed in beforeEach: userEvent
    // would swap navigator.clipboard for its own.
    fireEvent.click(screen.getByRole('button', { name: '复制调用地址' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/v1`))
    expect(
      await screen.findByRole('button', { name: '已复制调用地址' }),
    ).toBeInTheDocument()
  })

  it('运营后台不显示调用地址', () => {
    signIn()
    renderShell(<AdminLayout />, '/')

    expect(screen.queryByText('调用地址')).not.toBeInTheDocument()
  })
})
