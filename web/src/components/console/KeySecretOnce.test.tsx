import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeySecretOnce } from './KeySecretOnce'

const writeText = vi.fn().mockResolvedValue(undefined)

beforeEach(() => {
  writeText.mockClear()
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  })
})

afterEach(cleanup)

function batch(size: number) {
  return Array.from({ length: size }, (_, index) => ({
    id: index + 1,
    key_name: `env-${index + 1}`,
    key: `sk-secret-${index + 1}`,
  }))
}

describe('KeySecretOnce', () => {
  it('F6-3: shows the full plaintext and drops it when the dialog closes', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    render(<KeySecretOnce open keys={batch(1)} onClose={onClose} />)

    expect(screen.getByTestId('revealed-key-1')).toHaveTextContent('sk-secret-1')

    await user.click(screen.getByRole('button', { name: '我已保存，关闭' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('F6-3: renders nothing when there is no secret to reveal', () => {
    const { container } = render(<KeySecretOnce open keys={[]} onClose={() => undefined} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('F6-5: a batch of 10 can be copied in one click', async () => {
    const keys = batch(10)
    // fireEvent instead of userEvent: userEvent.setup() installs its own
    // clipboard stub and would hide the spy defined above.
    render(<KeySecretOnce open keys={keys} onClose={() => undefined} />)

    expect(screen.getAllByTestId(/^revealed-key-/)).toHaveLength(10)

    fireEvent.click(screen.getByRole('button', { name: '复制全部' }))

    expect(writeText).toHaveBeenCalledWith(keys.map((item) => item.key).join('\n'))
  })

  it('offers no copy-all button for a single key', () => {
    render(<KeySecretOnce open keys={batch(1)} onClose={() => undefined} />)
    expect(screen.queryByRole('button', { name: '复制全部' })).not.toBeInTheDocument()
  })
})
