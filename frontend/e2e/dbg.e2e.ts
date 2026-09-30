import { test, expect } from '@playwright/test'
import { login } from './helpers'
test('dbg', async ({ page }) => {
  test.setTimeout(120000)
  page.on('pageerror', (e) => console.log('PAGEERR', e.message.slice(0, 300)))
  page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE', m.text().slice(0, 200)) })
  page.on('crash', () => console.log('CRASH'))
  await login(page)
  await page.goto('/live')
  const rows = page.getByTestId('live-row')
  await expect(rows.first()).toBeVisible({ timeout: 20000 })
  await rows.first().dispatchEvent('click')
  for (let i = 0; i < 6; i++) {
    await page.waitForTimeout(1500)
    const info = await page.evaluate(() => ({
      url: location.href,
      dialogs: document.querySelectorAll('aside[role=dialog]').length,
      aria: document.querySelector('aside[role=dialog]')?.parentElement?.getAttribute('aria-hidden'),
      log: !!document.querySelector('aside[role=dialog] [role=log]'),
      text: (document.querySelector('aside[role=dialog]') as HTMLElement | null)?.innerText.slice(0, 120).replace(/\n/g, ' | '),
    }))
    console.log(i, JSON.stringify(info))
  }
  await page.keyboard.press('Escape')
  await page.waitForTimeout(2000)
  console.log('after esc', await page.evaluate(() => location.href), await page.getByRole('dialog').count())
})
