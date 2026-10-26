import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Table } from './Table'
import type { TableColumn } from './Table'

interface Row {
  id: string
  name: string
}

const columns: TableColumn<Row>[] = [
  {
    key: 'name',
    header: '名称',
    render: (row) => row.name,
    sortable: true,
    sortValue: (row) => row.name,
  },
]

describe('Table', () => {
  it('renders an error state instead of an empty table when a request fails', () => {
    const onRetry = vi.fn()
    render(
      <Table
        ariaLabel="密钥列表"
        columns={columns}
        rows={[]}
        rowKey={(row) => row.id}
        error="网络连接失败，请检查网络后重试。"
        onRetry={onRetry}
      />,
    )

    expect(screen.getByRole('alert')).toHaveTextContent('网络连接失败')
    expect(screen.queryByText('暂无数据')).not.toBeInTheDocument()
    expect(screen.queryByText('0')).not.toBeInTheDocument()
  })

  it('renders the empty state only for a successful empty response', () => {
    render(
      <Table
        ariaLabel="密钥列表"
        columns={columns}
        rows={[]}
        rowKey={(row) => row.id}
        emptyTitle="还没有密钥"
      />,
    )

    expect(screen.getByText('还没有密钥')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('sorts rows locally when no server-side sort handler is supplied', async () => {
    const user = userEvent.setup()
    const rows: Row[] = [
      { id: '2', name: 'beta' },
      { id: '1', name: 'alpha' },
    ]
    render(<Table ariaLabel="密钥列表" columns={columns} rows={rows} rowKey={(row) => row.id} />)

    expect(screen.getAllByRole('row')[1]).toHaveTextContent('beta')
    await user.click(screen.getByRole('button', { name: '按 名称 排序' }))
    expect(screen.getAllByRole('row')[1]).toHaveTextContent('alpha')
  })
})
