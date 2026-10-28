import { expect, test } from '@playwright/test'
import { registerUser, requestId, seedUsage } from './helpers'

/**
 * F19-1: the request picker is a real backend query over the session's own
 * t_usage_record rows (user_id AND created_at >= now-90d). The candidate list
 * must contain B's request and never A's, and its length must equal B's own
 * 90-day count (one seeded row).
 */
test('F19-1: 请求选择器只列本人 90 天 request_id', async ({ browser }) => {
  const a = await registerUser(browser, 'f19a')
  const b = await registerUser(browser, 'f19b')
  try {
    const aRequest = requestId('a')
    const bRequest = requestId('b')
    // The gateway writes t_usage_record; there is no API to create a usage row,
    // so the e2e fixture writes exactly the row the gateway would.
    seedUsage(a.userId, aRequest)
    seedUsage(b.userId, bRequest)

    await b.page.goto('/console/tickets')
    await b.page.getByRole('button', { name: '新建工单' }).first().click()

    const dialog = b.page.getByRole('dialog')
    await dialog.getByLabel('类型').selectOption('TECH')

    const picker = dialog.getByRole('list', { name: '可关联请求列表' })
    await expect(picker).toBeVisible()

    const candidateCount = await picker.getByRole('button').count()
    console.log(`[F19-1] candidate buttons: ${candidateCount}`)
    expect(candidateCount, 'B has exactly one 90-day request').toBe(1)

    await expect(picker.getByText(bRequest)).toBeVisible()
    await expect(dialog.getByText(aRequest)).toHaveCount(0)
  } finally {
    await a.context.close()
    await b.context.close()
  }
})
