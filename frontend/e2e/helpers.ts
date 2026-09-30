import { expect, type Page } from '@playwright/test'

export const ADMIN_EMAIL = process.env.E2E_EMAIL ?? 'admin@callgo.mn'
export const ADMIN_PASSWORD = process.env.E2E_PASSWORD ?? 'admin1234'

/** Signs in through the login form and waits for the app shell. */
export async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[name="email"]').fill(ADMIN_EMAIL)
  await page.locator('input[name="password"]').fill(ADMIN_PASSWORD)
  await page.locator('form button[type="submit"]').click()
  await expect(page.getByRole('complementary', { name: 'Үндсэн цэс' })).toBeVisible()
  await expect(page).not.toHaveURL(/\/login/)
}

/** A CSV campaign target list with `rows` numbers (phone,name,note). */
export function targetsCsv(rows: number): Buffer {
  const lines = ['phone,name,note']
  for (let i = 0; i < rows; i++) {
    lines.push(`+9769911${String(1000 + i)},Test Customer ${i + 1},note ${i + 1}`)
  }
  return Buffer.from(lines.join('\n') + '\n', 'utf8')
}

/** Unique-per-run suffix so reruns against a persistent database never collide. */
export const runId = (): string => Date.now().toString(36)
