import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useRealtimeMetric } from './useRealtimeMetric'

function Probe({ fetcher, intervalMs = 100 }: { fetcher: () => Promise<number>; intervalMs?: number }) {
  const { value, error, loading } = useRealtimeMetric(fetcher, { intervalMs })
  return (
    <div>
      <span data-testid="value">{value === null ? 'null' : String(value)}</span>
      <span data-testid="error">{error ?? ''}</span>
      <span data-testid="loading">{loading ? 'yes' : 'no'}</span>
    </div>
  )
}

describe('useRealtimeMetric', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('F7-2: polls on the configured interval and surfaces the newest value', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(10).mockResolvedValue(11)
    render(<Probe fetcher={fetcher} />)

    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByTestId('value')).toHaveTextContent('10')

    await act(async () => {
      vi.advanceTimersByTime(100)
      await Promise.resolve()
    })
    expect(screen.getByTestId('value')).toHaveTextContent('11')
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('returns null rather than 0 when a poll fails (hard constraint 6)', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(7)
      .mockRejectedValueOnce(new Error('network down'))
    render(<Probe fetcher={fetcher} />)

    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByTestId('value')).toHaveTextContent('7')

    await act(async () => {
      vi.advanceTimersByTime(100)
      await Promise.resolve()
    })

    expect(screen.getByTestId('value')).toHaveTextContent('null')
    expect(screen.getByTestId('value')).not.toHaveTextContent('0')
    expect(screen.getByTestId('error').textContent).not.toBe('')
  })

  it('does not poll when disabled', async () => {
    const fetcher = vi.fn().mockResolvedValue(1)
    render(<ProbeWithEnabled fetcher={fetcher} />)
    await act(async () => {
      vi.advanceTimersByTime(500)
      await Promise.resolve()
    })
    expect(fetcher).not.toHaveBeenCalled()
  })
})

function ProbeWithEnabled({ fetcher }: { fetcher: () => Promise<number> }) {
  const { value } = useRealtimeMetric(fetcher, { enabled: false })
  return <span data-testid="value">{value === null ? 'null' : String(value)}</span>
}
