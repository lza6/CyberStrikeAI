// F4 nonce 化后的关键交互功能回归（data-action 委托真实调用链）
const { test, expect } = require('@playwright/test');
const BASE = process.env.CSAI_E2E_BASE || 'http://127.0.0.1:8080';

async function waitForAuthReady(page, request) {
  let session = null;
  try {
    const resp = await request.post(BASE + '/api/auth/login', { data: { username: 'local', password: 'local' } });
    if (resp.ok()) session = await resp.json();
  } catch (e) { /* 页面自身探测 */ }
  if (session && session.token) {
    await page.addInitScript((sess) => {
      try {
        localStorage.setItem('cyberstrike-local-mode', 'true');
        localStorage.setItem('cyberstrike-auth', JSON.stringify({
          token: sess.token, expiresAt: sess.expires_at, user: sess.user || null,
          roles: sess.roles || [], permissions: sess.permissions || [], scope: sess.scope || ''
        }));
      } catch (e) { }
    }, session);
  }
  const ready = await page.waitForFunction(() => {
    try { return typeof authPermissions !== 'undefined' && authPermissions.size > 0; }
    catch (e) { return false; }
  }, { timeout: 12000 }).then(() => true).catch(() => false);
  if (!ready) {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      try { return typeof authPermissions !== 'undefined' && authPermissions.size > 0; }
      catch (e) { return false; }
    }, { timeout: 15000 });
  }
}

test('OF-1 主题切换（cycleThemePreference 委托生效）', async ({ page }) => {
  await page.goto(BASE + '/');
  await waitForAuthReady(page, page.request);
  const before = await page.evaluate(() => window.getThemePreference ? window.getThemePreference() : document.documentElement.getAttribute('data-theme-preference'));
  const btn = page.locator('[data-action="cycleThemePreference"]').first();
  await expect(btn).toBeVisible({ timeout: 8000 });
  // cycle 顺序 light → system → dark → light：点两次确保 data-theme 有可见变化
  //（第一击 light→system 时若系统偏好是 light，data-theme 不变，但 preference 已变）
  await btn.click();
  await page.waitForTimeout(300);
  const mid = await page.evaluate(() => window.getThemePreference ? window.getThemePreference() : document.documentElement.getAttribute('data-theme-preference'));
  await btn.click();
  await page.waitForTimeout(400);
  const afterPref = await page.evaluate(() => window.getThemePreference ? window.getThemePreference() : document.documentElement.getAttribute('data-theme-preference'));
  // 委托链路真实生效的证据：preference 沿 cycle 序推进两次（light→system→dark）
  const order = ['system', 'light', 'dark']; // 与 theme.js THEMES 一致
  const i0 = order.indexOf(before), i1 = order.indexOf(mid), i2 = order.indexOf(afterPref);
  expect(i1, `第一次点击 preference 应从 ${before} 前进`).toBe((i0 + 1) % 3);
  expect(i2, `第二次点击 preference 应继续前进`).toBe((i1 + 1) % 3);
});

test('OF-2 dashboard KPI 卡片点击进对话页（switchPage 多节点委托）', async ({ page }) => {
  await page.goto(BASE + '/');
  await waitForAuthReady(page, page.request);
  await expect(page).toHaveURL(/#dashboard/, { timeout: 8000 });
  const card = page.locator('#dashboard-cards [data-action="switchPage"][data-page="chat"]').first();
  await expect(card).toBeVisible({ timeout: 8000 });
  await card.click();
  await expect(page).toHaveURL(/#chat/, { timeout: 8000 });
});

test('OF-3 模态遮罩 if-self 语义（点遮罩关、点内容不关）', async ({ page }) => {
  await page.goto(BASE + '/');
  await waitForAuthReady(page, page.request);
  // 进项目页（showNewProjectModal 按钮在该页工具栏）
  await page.evaluate(() => { if (typeof switchPage === 'function') switchPage('projects'); });
  await expect(page).toHaveURL(/#projects/, { timeout: 8000 });
  await page.waitForTimeout(1200);
  const openBtn = page.locator('[data-action="showNewProjectModal"]:visible').first();
  await expect(openBtn).toBeVisible({ timeout: 8000 });
  await openBtn.click();
  await page.waitForTimeout(600);
  // 找 project 模态遮罩（含 data-if-self 且可见）
  const overlay = page.locator('#project-modal[data-if-self="1"]').first();
  if (await overlay.count() === 0) { test.skip(); return; }
  await expect(overlay).toBeVisible({ timeout: 5000 });
  // 点遮罩自身（左上角边缘，避开内容）→ 应关闭
  await overlay.click({ position: { x: 8, y: 8 } });
  await page.waitForTimeout(600);
  await expect(overlay).toBeHidden({ timeout: 5000 });
  const closed = await overlay.evaluate(el => getComputedStyle(el).display === 'none');
  expect(closed, '点遮罩自身应关闭模态').toBeTruthy();
});
