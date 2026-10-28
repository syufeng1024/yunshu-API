import { useState } from 'react'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { Button } from '@/components'
import type { Region } from '@/api/admin'
import { SourceRegionGuard } from './SourceRegionGuard'

afterEach(cleanup)

function Harness() {
  const [region, setRegion] = useState<Region>('DOMESTIC')
  return (
    <SourceRegionGuard
      value={region}
      onChange={setRegion}
      renderActions={({ blocked }) => <Button disabled={blocked}>保存</Button>}
    />
  )
}

describe('SourceRegionGuard', () => {
  it('SourceRegionGuard：选择境外即拦截保存', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    expect(screen.getByRole('button', { name: '保存' })).toBeEnabled()
    expect(screen.queryByTestId('source-region-blocked')).not.toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText(/来源地/), 'OVERSEAS')

    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
    expect(screen.getByTestId('source-region-blocked')).toHaveTextContent(
      'V1.0 仅支持境内渠道',
    )
  })
})
