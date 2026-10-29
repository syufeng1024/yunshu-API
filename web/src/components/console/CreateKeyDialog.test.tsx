import { cleanup, fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import { renderWithProviders } from '@/test/renderConsole'
import { CreateKeyDialog } from './CreateKeyDialog'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

const model = {
  id: 7,
  model_name: 'deepseek-chat',
  display_name: 'DeepSeek Chat',
  model_type: 'TEXT',
  context_length: 65_536,
  protocol: 'OPENAI_COMPAT',
  aliases: [],
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/console/models?')) {
      return Promise.resolve({ items: [model], page: 1, page_size: 100, total: 1 })
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
})

afterEach(cleanup)

describe('CreateKeyDialog', () => {
  it('F6-2: 从第 4 步回退到第 3 步后，前三步已填的名称、模型范围与到期时间都保留', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateKeyDialog open onClose={vi.fn()} onCreated={vi.fn()} />)

    const currentStep = () =>
      screen.getByRole('list', { name: '创建步骤' }).querySelector('[aria-current="step"]')

    // 第 1 步：名称
    await user.type(screen.getByLabelText(/密钥名称/), '生产密钥')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(currentStep()).toHaveTextContent('模型范围')

    // 第 2 步：指定模型
    await user.selectOptions(screen.getByLabelText(/模型范围/), 'SELECTED')
    await user.click(await screen.findByRole('checkbox', { name: /DeepSeek Chat/ }))
    expect(screen.getByText('已选择 1 个模型。')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(currentStep()).toHaveTextContent('有效期')

    // 第 3 步：到期时间
    fireEvent.change(screen.getByLabelText(/到期时间/), {
      target: { value: '2030-01-01T10:00' },
    })
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(currentStep()).toHaveTextContent('IP 白名单')
    expect(screen.getByLabelText('IP 白名单')).toBeInTheDocument()

    // 第 4 步 → 第 3 步：到期时间仍在
    await user.click(screen.getByRole('button', { name: '上一步' }))
    expect(currentStep()).toHaveTextContent('有效期')
    expect(screen.getByLabelText(/到期时间/)).toHaveValue('2030-01-01T10:00')

    // 第 3 步 → 第 2 步：模型范围与已选模型仍在
    await user.click(screen.getByRole('button', { name: '上一步' }))
    expect(screen.getByLabelText(/模型范围/)).toHaveValue('SELECTED')
    expect(screen.getByText('已选择 1 个模型。')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /DeepSeek Chat/ })).toBeChecked()

    // 第 2 步 → 第 1 步：名称仍在
    await user.click(screen.getByRole('button', { name: '上一步' }))
    expect(screen.getByLabelText(/密钥名称/)).toHaveValue('生产密钥')
  })
})
