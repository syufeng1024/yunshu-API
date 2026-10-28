import { expect, test } from '@playwright/test'
import { createKey, registerUser } from './helpers'

/**
 * F7-1: two independent browser contexts with their own sid cookies. B must
 * not be able to distinguish "A's key" from "no such key": both pages render
 * the same visible text, including the backend's key_not_found copy.
 */
test("F7-1: B 访问 A 的 Key 与不存在 Key 的可见文本逐字一致", async ({ browser }) => {
  const a = await registerUser(browser, 'f71a')
  const b = await registerUser(browser, 'f71b')
  try {
    const key = await createKey(a.context, a.csrf, 'e2e-f71-key')

    await b.page.goto(`/console/keys/${key.id}`)
    await expect(b.page.getByRole('heading', { name: '密钥详情' })).toBeVisible()
    await expect(b.page.getByText('无法加载密钥')).toBeVisible()
    const foreignText = await b.page.locator('body').innerText()

    await b.page.goto('/console/keys/999999999')
    await expect(b.page.getByText('无法加载密钥')).toBeVisible()
    const missingText = await b.page.locator('body').innerText()

    console.log(`[F7-1] A key URL as B:\n${foreignText}`)
    console.log(`[F7-1] random missing id as B:\n${missingText}`)
    expect(foreignText).toBe(missingText)
  } finally {
    await a.context.close()
    await b.context.close()
  }
})
