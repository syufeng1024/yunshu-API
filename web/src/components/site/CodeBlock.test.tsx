import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CodeBlock } from './CodeBlock'

const writeText = vi.fn<(text: string) => Promise<void>>()

beforeEach(() => {
  writeText.mockReset()
  writeText.mockResolvedValue(undefined)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('CodeBlock', () => {
  it('F3-1: copies the code byte-for-byte and shows the copied label', async () => {
    const user = userEvent.setup()
    // userEvent installs its own clipboard stub during setup; replace it after.
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const code = 'const value = 1\n  return value\n'
    render(<CodeBlock code={code} label="示例" />)

    await user.click(screen.getByRole('button', { name: '复制代码' }))

    expect(writeText).toHaveBeenCalledWith(code)
    expect(screen.getByText('已复制')).toBeInTheDocument()
  })
})
