import { expect, test } from '@playwright/test'
import { formatPricePerK } from '../src/lib/format'

/**
 * G-12: the public site must render the price that `GET /api/public/models`
 * reports, not a stale copy. The table lives on the landing page
 * (`data-section="models"`, heading「模型价格表」, description「单价为标准分组
 * 公开价」); /pricing itself only renders plan cards and the billing FAQ.
 *
 * Both sides use `formatPricePerK` — the same helper PriceTable.tsx calls — so
 * the assertion is about the value that reaches the DOM, not about a second
 * copy of the ÷10,000 conversion.
 *
 * Requires the full stack to be up (nginx + controld). CI has no such stack,
 * so the case is skipped there unless E2E_BASE_URL is provided.
 */

interface PublicModel {
  model_name: string
  model_type: string
  listing_state: string
  input_price_micro_per_k: number
}

test('G-12: 官网「标准分组」价格表的输入单价与 /api/public/models 换算一致', async ({
  page,
  request,
}) => {
  test.skip(
    Boolean(process.env.CI) && !process.env.E2E_BASE_URL,
    'G-12 需要外部已启动的完整栈：设置 E2E_BASE_URL 后运行',
  )

  const response = await request.get('/api/public/models')
  expect(response.ok(), `GET /api/public/models -> ${response.status()}`).toBeTruthy()
  const body = (await response.json()) as { data: PublicModel[] }
  expect(
    body.data.some(
      (model) => model.listing_state === 'LISTED' && model.model_type === 'TEXT',
    ),
    '接口至少返回一个已上架文本模型',
  ).toBeTruthy()

  await page.goto('/')
  const table = page.getByRole('table', { name: '模型价格表' })
  await expect(table).toBeVisible()

  const headers = (await table.locator('thead th').allInnerTexts()).map((text) => text.trim())
  const priceColumn = headers.indexOf('输入单价')
  expect(priceColumn, '价格表必须包含「输入单价」列').toBeGreaterThanOrEqual(0)

  // Default tab is TEXT and rows are sorted by recommend_weight DESC, so the
  // first row is the first listed text model the visitor sees.
  const firstRow = table.locator('tbody tr').first()
  const modelNameCell = firstRow.locator('p.font-mono')
  await expect(modelNameCell).toBeVisible()

  const renderedModelName = (await modelNameCell.innerText()).trim()
  const apiModel = body.data.find((model) => model.model_name === renderedModelName)
  expect(apiModel, `接口应返回页面展示的模型 ${renderedModelName}`).toBeTruthy()

  const renderedPrice = (await firstRow.locator('td').nth(priceColumn).innerText()).trim()
  expect(renderedPrice).toBe(formatPricePerK(apiModel!.input_price_micro_per_k))
})
