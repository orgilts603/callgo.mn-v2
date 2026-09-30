import { test, expect } from '@playwright/test'
import { login } from './helpers'
test('dbg', async ({ page }) => {
  await login(page)
  await page.goto('/live')
  const rows = page.getByTestId('live-row')
  await expect(rows.first()).toBeVisible({ timeout: 20000 })
  await rows.first().click()
  await page.waitForTimeout(1000)
  const rafs: number[] = []
  for (let i = 0; i < 5; i++) rafs.push(await page.evaluate(() => new Promise<number>((res) => { const s = performance.now(); requestAnimationFrame(() => res(performance.now() - s)) })))
  console.log('RAF', rafs.map((n) => Math.round(n)).join(','))
  const t0 = Date.now()
  await page.evaluate(() => (document.querySelector('aside[role=dialog] button[aria-label=Close]') as HTMLElement).click())
  console.log('URL', page.url(), Date.now() - t0)
  await page.waitForTimeout(1500)
  console.log('URL after', page.url(), 'dialogs', await page.getByRole('dialog').count())
})
