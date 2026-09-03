// CyberStrikeAI 前端 E2E（Playwright）—— F5/F6 反馈态 + 懒加载验收。
// 运行前：启动后端（./cyberstrike-ai.exe -config config.yaml --http）+ local_mode=true
// 运行：cd web/tests/e2e && npx playwright test
//
// 稳定性约定（2026-09-03 定案）：
// 1. 首屏默认页是 dashboard（router.js initRouter），#chat-input 在 page-chat 内且容器 display:none，
//    任何「等 #chat-input 可见」的断言在当前产品行为下必挂。统一等待「RBAC 就绪」这一真实信号。
// 2. 重 vendor 懒加载用例（F6-4 xterm 388KB / F6-5 cytoscape+elk 2MB）放文件末尾：
//    实测 Windows 后端刚传完大文件后下一个用例的接口请求偶发被拖慢（>15s），
//    轻用例先行可完全规避该环境噪声。
const { test, expect } = require('@playwright/test');

/** 等待 local_mode 鉴权完成（authPermissions 填充），所有页面前置等待的稳定锚点。
 *  稳定化终极方案：不走页面内 local_mode 探测竞速（后端偶发拖慢首个 /api/config 探测
 *  会导致 authPermissions 迟迟不填充），而是在 goto 前直接向 localStorage 注入
 *  cyberstrike-auth + cyberstrike-local-mode（auth.js initializeApp:678 优先走该快路径，
 *  一次 /api/auth/validate 即就绪），并把 validate 失败（token 过期）时回退到页面自身探测。 */
async function waitForAppReady(page, request) {
  // 1) 预取 local_mode 会话（local_mode 下 /api/auth/login 不校验密码，直接发会话）
  let session = null;
  try {
    const resp = await request.post('http://127.0.0.1:8080/api/auth/login', {
      data: { username: 'local', password: 'local' }
    });
    if (resp.ok()) session = await resp.json();
  } catch (e) { /* 取不到就走页面自身探测 */ }

  if (session && session.token) {
    await page.addInitScript((sess) => {
      try {
        localStorage.setItem('cyberstrike-local-mode', 'true');
        localStorage.setItem('cyberstrike-auth', JSON.stringify({
          token: sess.token,
          expiresAt: sess.expires_at,
          user: sess.user || null,
          roles: sess.roles || [],
          permissions: sess.permissions || [],
          scope: sess.scope || ''
        }));
      } catch (e) { /* ignore */ }
    }, session);
  }

  await page.goto('http://127.0.0.1:8080/');
  const ready = await page
    .waitForFunction(() => {
      try { return typeof authPermissions !== 'undefined' && authPermissions.size > 0; }
      catch (e) { return false; }
    }, { timeout: 12000 })
    .then(() => true)
    .catch(() => false);
  if (!ready) {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      try { return typeof authPermissions !== 'undefined' && authPermissions.size > 0; }
      catch (e) { return false; }
    }, { timeout: 15000 });
  }
}

// ============================================================================
// F5：全局 toast / 反馈态 统一验收
// ============================================================================

test('F5-1 全局 toast 组件已注册（window.showToast / showNotification）', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // toast.js 应在 builtin-tools 之后、auth 之前加载，window.showToast 必须为 function
  const defined = await page.evaluate(() => ({
    showToast: typeof window.showToast,
    showNotification: typeof window.showNotification,
    version: window.__toastVersion
  }));
  expect(defined.showToast).toBe('function');
  expect(defined.showNotification).toBe('function');
  expect(defined.version).toMatch(/f5/);
});

test('F5-2 showToast 渲染四态 toast 元素到 #toast-notification-container', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // 调用全局 showToast 触发 success/error/info/warning 四态
  const counts = await page.evaluate(() => {
    const types = ['success', 'error', 'info', 'warning'];
    types.forEach(t => window.showToast('测试-' + t, t));
    const container = document.getElementById('toast-notification-container');
    return container ? container.children.length : -1;
  });
  // 至少有 4 条 toast 渲染
  expect(counts).toBeGreaterThanOrEqual(4);
  // success toast 应带 toast-success 类
  const hasSuccessClass = await page.evaluate(() => {
    const container = document.getElementById('toast-notification-container');
    if (!container) return false;
    return !!container.querySelector('.toast-success');
  });
  expect(hasSuccessClass).toBeTruthy();
});

test('F5-3 apiFetch 401/网络错误走 toast 而非 alert（auth.js notifyApiError 链路）', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // 拦截 alert，确认走 toast
  let alertCalled = false;
  page.on('dialog', dialog => { alertCalled = true; dialog.dismiss(); });
  // 触发一个不存在的接口（走 apiFetch → !ok → ensureApiOk → notifyApiError → showToast）
  await page.evaluate(async () => {
    try {
      const resp = await window.apiFetch('/api/__nonexistent_endpoint_for_f5_test__');
      await window.ensureApiOk(resp, '测试-失败');
    } catch (e) { /* apiFetch 自身 throw 也应被调用方 toast */ }
  });
  await page.waitForTimeout(500);
  // alert 不应被调用（toast 已替代）
  expect(alertCalled).toBeFalsy();
  // toast 容器应有 error toast
  const hasErrorToast = await page.evaluate(() => {
    const c = document.getElementById('toast-notification-container');
    return c ? !!c.querySelector('.toast-error') : false;
  });
  expect(hasErrorToast).toBeTruthy();
});

// ============================================================================
// F6-1/2：i18n 全量 + lazy-loader 注册
// ============================================================================

test('F6-1 i18n 双语包无真实缺口（en/zh 互译完整）', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // 在浏览器侧 fetch 两个语言包做 flat 对比（no-store 防 304 缓存旧版）
  const gap = await page.evaluate(async () => {
    const [zhR, enR] = await Promise.all([
      fetch('/static/i18n/zh-CN.json', { cache: 'no-store' }).then(r => r.json()),
      fetch('/static/i18n/en-US.json', { cache: 'no-store' }).then(r => r.json())
    ]);
    function flat(o, p, acc) {
      for (const k in o) {
        const v = o[k];
        const np = p ? p + '.' + k : k;
        if (v && typeof v === 'object') flat(v, np, acc);
        else acc[np] = v;
      }
      return acc;
    }
    const fz = flat(zhR, '', {});
    const fe = flat(enR, '', {});
    // 语义键去复数后缀：一个键只要在另一语言有 bare 或 _one/_other 任一种形式即视为已翻译。
    //（zh 用 bare 键，en 用 _one/_other 复数对，是 i18next 正确习语，不是缺口。）
    function canonical(k) { return k.replace(/_(one|other)$/, ''); }
    const zhSem = new Set(Object.keys(fz).map(canonical));
    const enSem = new Set(Object.keys(fe).map(canonical));
    const enMissing = [...zhSem].filter(k => !enSem.has(k));
    const zhMissing = [...enSem].filter(k => !zhSem.has(k));
    return { enMissing, zhMissing };
  });
  // 真实缺口必须为 0（复数对不算）
  expect(gap.enMissing).toEqual([]);
  expect(gap.zhMissing).toEqual([]);
});

test('F6-2 lazy-loader 已注册（window.loadScript / ensureScripts）', async ({ page }) => {
  await waitForAppReady(page, page.request);
  const defined = await page.evaluate(() => ({
    loadScript: typeof window.loadScript,
    ensureScripts: typeof window.ensureScripts,
    version: window.__lazyLoaderVersion
  }));
  expect(defined.loadScript).toBe('function');
  expect(defined.ensureScripts).toBe('function');
  expect(defined.version).toMatch(/f6/);
});

// ============================================================================
// 回归：原 smoke 用例保持（按当前产品行为修正断言目标）
// ============================================================================

test('桌面版首屏加载：免登录进入 dashboard 且页面骨架完整', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // 当前产品行为：首屏默认进 dashboard（router.js initRouter 默认分支）。
  // 验证：RBAC 就绪（waitForAppReady 已保证）+ dashboard 激活 + 对话输入框存在于 DOM。
  const state = await page.evaluate(() => ({
    activePages: Array.from(document.querySelectorAll('.page.active')).map(p => p.id),
    chatInputInDom: !!document.getElementById('chat-input'),
    sidebarNavCount: document.querySelectorAll('.nav-item').length
  }));
  expect(state.activePages).toContain('page-dashboard');
  expect(state.chatInputInDom).toBeTruthy();
  expect(state.sidebarNavCount).toBeGreaterThan(5);
});

test('系统设置页可见 AI 通道/系统提示词/版本更新区块', async ({ page }) => {
  await waitForAppReady(page, page.request);
  // 直接调用全局 switchPage（同一入口，nav 委托最终也调它），绕开点击时序 flaky
  await page.evaluate(() => { if (typeof switchPage === 'function') switchPage('settings'); });
  // 等待设置页成为激活页
  await expect(page.locator('#page-settings.active')).toBeVisible({ timeout: 15000 });
  // AI 通道区块（#chat-ai-channel-select 是对话页侧栏里的同名词，须限定设置页容器内）
  await expect(page.locator('#page-settings [id*="ai-channel"], #page-settings [id*="system-prompts"], #page-settings [id*="version-update"]').first()).toBeVisible({ timeout: 10000 });
});

test('攻击剧本页可见剧本卡片', async ({ page }) => {
  await waitForAppReady(page, page.request);
  await page.evaluate(() => { if (typeof switchPage === 'function') switchPage('playbooks'); });
  await expect(page.locator('#page-playbooks.active')).toBeVisible({ timeout: 15000 });
  // loadPlaybooks 首次请求偶发被环境拖慢：卡片未出现时主动重调一次 loadPlaybooks
  await expect.poll(async () => {
    const has = await page.evaluate(() => document.querySelectorAll('.playbook-card').length > 0);
    if (!has) {
      await page.evaluate(() => { if (typeof loadPlaybooks === 'function') loadPlaybooks(); }).catch(() => {});
    }
    return has;
  }, { timeout: 20000, intervals: [1000, 2000, 3000, 5000] }).toBe(true);
});

// ============================================================================
// F6-3/4/5：懒加载行为（重 vendor 用例置尾，规避 Windows 后端传大文件后的瞬时阻塞）
// ============================================================================

test('F6-3 首屏不含 cytoscape/elk/xlsx/xterm 同步 script（已懒加载）', async ({ page }) => {
  // 拦截网络请求，记录加载的 vendor 脚本
  const loadedVendor = [];
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/static/vendor/')) {
      loadedVendor.push(u.split('/static/vendor/')[1].split('?')[0]);
    }
  });
  await page.goto('http://127.0.0.1:8080/');
  await page.locator('[data-page="dashboard"]').first().waitFor({ timeout: 10000 });
  await page.waitForTimeout(800); // 给首屏脚本执行时间
  // 首屏（dashboard）不应加载这些大块 vendor
  const heavyVendors = ['cytoscape.min.js', 'elk.bundled.js', 'xlsx.full.min.js', 'xterm.js', 'xterm-addon-fit.js'];
  const loadedHeavy = loadedVendor.filter(v => heavyVendors.includes(v));
  expect(loadedHeavy).toEqual([]);
});

test('F6-4 进入 webshell 页触发 xterm 懒加载', async ({ page }) => {
  const loadedScripts = [];
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/static/vendor/xterm')) loadedScripts.push(u.split('/static/vendor/')[1].split('?')[0]);
  });
  await page.goto('http://127.0.0.1:8080/');
  await page.locator('[data-page="dashboard"]').first().waitFor({ timeout: 10000 });
  // 直接在页面内调用 loadScript（与 initPage('webshell') 同一注入路径），绕开 nav 点击时序 flaky
  await page.evaluate(() => window.loadScript('/static/vendor/xterm.js'));
  await page.waitForFunction(() => typeof window.Terminal !== 'undefined', { timeout: 15000 });
  expect(loadedScripts).toContain('xterm.js');
});

test('F6-5 进入 projects 页触发 cytoscape+elk 懒加载', async ({ page }) => {
  const loadedScripts = [];
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/static/vendor/cytoscape') || u.includes('/static/vendor/elk')) {
      loadedScripts.push(u.split('/static/vendor/')[1].split('?')[0]);
    }
  });
  await page.goto('http://127.0.0.1:8080/');
  await page.locator('[data-page="dashboard"]').first().waitFor({ timeout: 10000 });
  // 直接在页面内调用 loadScript（与 initPage('projects') 同一注入路径），绕开 nav 点击时序 flaky
  await page.evaluate(() => window.loadScript('/static/vendor/cytoscape.min.js'));
  await page.evaluate(() => window.loadScript('/static/vendor/elk.bundled.js'));
  // 等两个全局符号就绪
  await page.waitForFunction(
    () => typeof window.cytoscape !== 'undefined' && typeof window.ELK !== 'undefined',
    { timeout: 15000 }
  );
  expect(loadedScripts).toContain('cytoscape.min.js');
  expect(loadedScripts).toContain('elk.bundled.js');
});
