// Playwright 配置 — CyberStrikeAI 前端冒烟
// 后端需先启动：./cyberstrike-ai.exe -config config.yaml --http (local_mode=true)
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: '.',
  // 90s：Windows 本机后端偶发瞬时挂起（连 /api/auth/validate 都可能 >12s），
  // waitForAppReady 12s 等 + reload + 15s 复等需要更大用例预算
  timeout: 90000,
  expect: { timeout: 15000 },
  fullyParallel: false,
  retries: 1,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.CSAI_E2E_BASE || 'http://127.0.0.1:8080',
    headless: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
});
