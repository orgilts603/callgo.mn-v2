// Playwright configuration for the CallGo.mn smoke tests.
//
// The test project lives in ./e2e with its own package.json / package-lock.json
// (npm) so the frontend's pnpm manifest stays untouched:
//
//   cd frontend/e2e && npm ci && npx playwright install --with-deps chromium
//   E2E_BASE_URL=http://127.0.0.1:5173 npm test
//
// Environment:
//   E2E_BASE_URL       where the dashboard is served (default http://127.0.0.1:5173)
//   E2E_EMAIL / E2E_PASSWORD  seeded admin (default admin@callgo.mn / admin1234)
//   CALLGO_E2E_CHROME  path of a Chromium binary to use instead of Playwright's own
//
// Only a type import from '@playwright/test' is used here: it is erased at
// runtime, so this file loads even though the package is installed in ./e2e.
import type { PlaywrightTestConfig } from '@playwright/test'

const chrome = process.env.CALLGO_E2E_CHROME
const baseURL = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5173'

const config: PlaywrightTestConfig = {
  testDir: './e2e',
  testMatch: /.*\.e2e\.ts$/,
  outputDir: './e2e/test-results',
  // Specs share one backend and create data; run them one after another.
  fullyParallel: false,
  workers: 1,
  retries: 1,
  forbidOnly: !!process.env.CI,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI
    ? [['github'], ['html', { outputFolder: './e2e/playwright-report', open: 'never' }]]
    : [['list']],
  use: {
    baseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'off',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    launchOptions: chrome ? { executablePath: chrome } : {},
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium', viewport: { width: 1440, height: 900 } } }],
}

export default config
