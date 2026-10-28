import { expect, test } from '@playwright/test'
import { createKey, registerUser } from './helpers'

/**
 * F4-2: once the one-time secret dialog is gone, the key detail page must not
 * contain the plaintext in any observable layer. The create response is the
 * only legitimate carrier of the plaintext; the observation window starts
 * after the key is created and covers the five required places:
 * DOM, URL, localStorage, sessionStorage and network response bodies.
 */
test('F4-2: 密钥详情页 DOM/URL/localStorage/sessionStorage/network 均无明文 Key', async ({
  browser,
}) => {
  const user = await registerUser(browser, 'f42')
  try {
    const key = await createKey(user.context, user.csrf, 'e2e-f42-key')
    const page = user.page

    const responseBodies: string[] = []
    const pending: Array<Promise<void>> = []
    page.on('response', (response) => {
      pending.push(
        response
          .text()
          .then((body) => {
            responseBodies.push(body)
          })
          .catch(() => {
            // Redirects and aborted bodies have no readable text.
          }),
      )
    })

    await page.goto(`/console/keys/${key.id}`)
    await expect(page.getByText(key.key_masked, { exact: true }).first()).toBeVisible()
    await page.waitForLoadState('networkidle')
    // Let the listener's body reads settle before the snapshot.
    await page.waitForTimeout(300)
    await Promise.all(pending)

    const dom = await page.content()
    const url = page.url()
    const storage = await page.evaluate(() => ({
      local: { ...localStorage },
      session: { ...sessionStorage },
    }))
    const network = responseBodies.join('\n')

    expect(dom, 'DOM must not contain the full key').not.toContain(key.key)
    expect(url, 'URL must not contain the full key').not.toContain(key.key)
    expect(JSON.stringify(storage.local), 'localStorage must not contain the full key').not.toContain(
      key.key,
    )
    expect(
      JSON.stringify(storage.session),
      'sessionStorage must not contain the full key',
    ).not.toContain(key.key)
    expect(network, 'network response bodies must not contain the full key').not.toContain(
      key.key,
    )

    // Positive control: the page really rendered the mask, not a blank value.
    expect(dom).toContain(key.key_masked)
  } finally {
    await user.context.close()
  }
})
