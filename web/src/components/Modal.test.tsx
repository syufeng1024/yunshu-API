import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Modal } from './Modal'

describe('Modal', () => {
  it('traps focus and closes on Escape', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()

    render(
      <Modal
        open
        onClose={onClose}
        title="创建密钥"
        footer={<button type="button">保存</button>}
      >
        <button type="button">第一项</button>
        <button type="button">第二项</button>
      </Modal>,
    )

    expect(screen.getByRole('dialog', { name: '创建密钥' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '关闭弹窗' })).toHaveFocus()

    await user.tab({ shift: true })
    expect(screen.getByRole('button', { name: '保存' })).toHaveFocus()

    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
