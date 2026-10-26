import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { fixtureResponses } from '@/fixtures/public'
import { EndpointBlock } from './EndpointBlock'

const endpoints = fixtureResponses['/api/public/endpoints'].data

describe('EndpointBlock', () => {
  it('F1-4: shows only lines and Base URLs when no health data exists', () => {
    const { container } = render(<EndpointBlock endpoints={endpoints} />)

    expect(screen.getByText('https://api.yunshu.dev/v1')).toBeInTheDocument()
    expect(screen.getByText('https://backup.api.yunshu.dev/v1')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: '查看服务状态' })).toHaveLength(2)
    expect(container.textContent).not.toMatch(/可用|正常|在线/)
  })

  it('shows a health-check label only when the backend supplies a status', () => {
    const { container } = render(
      <EndpointBlock endpoints={[{ ...endpoints[0], health_status: 'PASS' }]} />,
    )
    expect(screen.getByText('健康检查通过')).toBeInTheDocument()
    expect(container.textContent).not.toMatch(/可用|正常|在线/)
  })

  it('renders ErrorState instead of an empty list when the request fails', () => {
    render(<EndpointBlock endpoints={[]} error="接入点加载失败" />)
    expect(screen.getByRole('alert')).toHaveTextContent('接入点加载失败')
    expect(screen.queryByText('暂无接入点配置')).not.toBeInTheDocument()
  })
})
