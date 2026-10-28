import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FloorCheckHint } from './FloorCheckHint'

afterEach(cleanup)

describe('FloorCheckHint', () => {
  it('FloorCheckHint：地板不满足显示差额', () => {
    render(
      <FloorCheckHint priceMicroPerK={1000} groupRatioBp={8500} costMicroPerK={800} />,
    )

    expect(screen.getByTestId('floor-check-difference')).toHaveTextContent('差额 217')
    expect(screen.getByTestId('floor-check-difference')).toHaveTextContent('积分/千 token')
    expect(screen.queryByText(/校验失败/)).not.toBeInTheDocument()
  })

  it('FloorCheckHint：满足地板时无告警', () => {
    render(
      <FloorCheckHint priceMicroPerK={1000} groupRatioBp={8500} costMicroPerK={600} />,
    )

    expect(screen.queryByTestId('floor-check-difference')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByTestId('floor-check-pass')).toHaveTextContent('毛利地板校验通过')
  })
})
