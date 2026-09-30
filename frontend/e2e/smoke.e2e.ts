import { expect, test } from '@playwright/test'
import { ADMIN_EMAIL, ADMIN_PASSWORD, login, runId, targetsCsv } from './helpers'

test.describe('CallGo.mn dashboard smoke', () => {
  test('login rejects a wrong password and accepts the seeded admin', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByRole('heading', { name: 'Системд нэвтрэх' })).toBeVisible()

    await page.locator('input[name="email"]').fill(ADMIN_EMAIL)
    await page.locator('input[name="password"]').fill(`${ADMIN_PASSWORD}-wrong`)
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toBeVisible()
    await expect(page).toHaveURL(/\/login/)

    await page.locator('input[name="password"]').fill(ADMIN_PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('complementary', { name: 'Үндсэн цэс' })).toBeVisible()
    await expect(page).not.toHaveURL(/\/login/)
  })

  test('protected pages redirect anonymous visitors to the login form', async ({ page }) => {
    await page.goto('/campaigns')
    await expect(page).toHaveURL(/\/login/)
  })

  test('dashboard renders the stat cards', async ({ page }) => {
    await login(page)
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Хяналтын самбар', level: 1 })).toBeVisible()
    for (const label of ['Нийт дуудлага', 'Идэвхтэй', 'Өнөөдөр дууссан', 'Дундаж хугацаа', 'Эерэг %', 'Сөрөг %']) {
      await expect(page.getByText(label, { exact: true }).first()).toBeVisible()
    }
    // Skeletons are replaced by real numbers once /api/stats answers.
    await expect(page.getByTestId('stat-skeleton')).toHaveCount(0)
  })

  test('live desk shows simulator calls within 20 s and opens the call drawer', async ({ page }) => {
    await login(page)
    await page.goto('/live')
    await expect(page.getByTestId('ws-status')).toHaveText('Шууд холбогдсон', { timeout: 15_000 })

    const rows = page.getByTestId('live-row')
    await expect(rows.first()).toBeVisible({ timeout: 20_000 })

    const callId = await rows.first().getAttribute('data-call-id')
    expect(callId).toBeTruthy()
    await rows.first().click()

    await expect(page).toHaveURL(new RegExp(`call=${callId}`))
    const drawer = page.getByRole('dialog')
    await expect(drawer).toBeVisible()
    // The drawer header shows "from → to" of the selected call.
    await expect(drawer.getByText(/\d/).first()).toBeVisible()
    await drawer.getByRole('button', { name: 'Close' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  test('creates a campaign from a generated CSV and lists it', async ({ page }) => {
    await login(page)
    await page.goto('/campaigns')
    await expect(page.getByRole('heading', { name: 'Кампанит ажил', level: 1 })).toBeVisible()

    const name = `E2E campaign ${runId()}`
    await page.getByRole('button', { name: 'Шинэ кампанит ажил' }).first().click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Шинэ кампанит ажил' })).toBeVisible()

    // Step 1: settings. The seeded demo line and profile are the only options.
    await dialog.getByPlaceholder('Жишээ: Долдугаар сарын сануулга').fill(name)
    await dialog.getByPlaceholder('Сайн байна уу {{name}}, ...').fill('Сайн байна уу {{name}}, энэ бол туршилт.')
    const selects = dialog.locator('select')
    await expect(selects.first().locator('option')).not.toHaveCount(1) // options loaded
    await selects.nth(0).selectOption({ index: 1 })
    if ((await selects.nth(1).inputValue()) === '') await selects.nth(1).selectOption({ index: 1 })
    await dialog.getByRole('button', { name: 'Үргэлжлүүлэх' }).click()

    // Steps 2 and 3 (schedule, outcomes) keep their valid defaults.
    await dialog.getByRole('button', { name: 'Үргэлжлүүлэх' }).click()
    await dialog.getByRole('button', { name: 'Үргэлжлүүлэх' }).click()

    // Step 4: upload the list and check the detected mapping.
    await dialog.locator('input[type="file"]').setInputFiles({
      name: 'targets.csv', mimeType: 'text/csv', buffer: targetsCsv(5),
    })
    await expect(dialog.getByTestId('phone-detected')).toBeVisible()
    await expect(dialog.getByTestId('csv-preview')).toContainText('5')
    await dialog.getByRole('button', { name: 'Үүсгэх' }).click()

    // Step 5: result summary, then back to the list.
    const result = dialog.getByTestId('result')
    await expect(result).toContainText(name)
    await expect(result).toContainText('Импортолсон')
    await dialog.getByRole('button', { name: 'Хаах' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await expect(page.getByRole('link', { name }).first()).toBeVisible()
  })

  test('settings: creates a knowledge base', async ({ page }) => {
    await login(page)
    await page.goto('/settings/knowledge')
    await expect(page.getByRole('heading', { name: 'Мэдлэгийн сангууд' })).toBeVisible()

    const name = `E2E knowledge ${runId()}`
    await page.getByRole('button', { name: 'Сан үүсгэх' }).first().click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Шинэ мэдлэгийн сан' })).toBeVisible()
    await dialog.getByPlaceholder('Бүтээгдэхүүний заавар').fill(name)
    await dialog.getByRole('button', { name: 'Хадгалах' }).click()

    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(page.getByRole('link', { name })).toBeVisible()
  })
})
