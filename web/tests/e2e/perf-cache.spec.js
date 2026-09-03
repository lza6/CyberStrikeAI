const { test, expect } = require('@playwright/test');

test('P1: 首屏静态 JS 走长缓存 immutable', async ({ page }) => {
  const seen = [];
  page.on('response', async (resp) => {
    const url = resp.url();
    if (url.includes('/static/js/') || url.includes('/static/css/')) {
      const cc = resp.headers()['cache-control'] || '';
      seen.push({ url: url.split('/').pop(), cc });
    }
  });
  await page.goto('http://127.0.0.1:8090/', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  expect(seen.length, '应至少捕获 1 个静态资源').toBeGreaterThan(0);
  for (const s of seen) {
    expect(s.cc, `${s.url} 应为长缓存`).toContain('max-age=31536000');
    expect(s.cc, `${s.url} 应含 immutable`).toContain('immutable');
  }
});

test('P2: 首页 HTML 不被长缓存（no-store）', async ({ page }) => {
  const resp = await page.goto('http://127.0.0.1:8090/', { waitUntil: 'domcontentloaded' });
  const cc = resp.headers()['cache-control'] || '';
  expect(cc, '首页应 no-store').toContain('no-store');
});

test('P3: /api/config 返回 JSON + no-store（不缓存配置）', async ({ request }) => {
  const resp = await request.get('http://127.0.0.1:8090/api/config');
  expect(resp.status()).toBe(200);
  const cc = resp.headers()['cache-control'] || '';
  expect(cc, 'api/config 应 no-store').toContain('no-store');
  const body = await resp.text();
  expect(body).toContain('tools');
  // 两次请求内容一致（cache 命中）
  const resp2 = await request.get('http://127.0.0.1:8090/api/config');
  const body2 = await resp2.text();
  expect(body2.length).toBe(body.length);
});
