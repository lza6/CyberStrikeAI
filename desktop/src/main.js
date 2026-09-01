// CyberStrikeAI 桌面启动器主进程
// 职责：
//   1. 启动前检查 AI 通道是否已配置（否则弹出配置窗口引导小白填 Key）
//   2. 拉起内嵌后端 cyberstrike-ai.exe，等待 ONLINE
//   3. 打开主 BrowserWindow 指向 http://127.0.0.1:8080
const { app, BrowserWindow, shell, Menu, ipcMain, dialog } = require('electron');
const path = require('path');
const fs = require('fs');
const net = require('net');
const { spawn } = require('child_process');
const ai = require('./ai-config');
const tray = require('./tray');

let backendProc = null;
let mainWindow = null;
let configWindow = null;
let splashWindow = null;
let backendExitedUnexpectedly = false;
let backendExitDialogShown = false;
let backendStartedAt = 0;

function rootDir() {
  return app.isPackaged ? process.resourcesPath : path.join(__dirname, '..', '..');
}

function cfgPath() { return path.join(rootDir(), 'config.yaml'); }
function iconPath() { return path.join(__dirname, '..', 'icons', 'icon.png'); }

// ---- 启动后端 ----
function startBackend() {
  const root = rootDir();
  const exe = path.join(root, 'cyberstrike-ai.exe');
  const pyHome = path.join(root, 'runtime', 'python', 'python-3.13.5');
  const cfg = cfgPath();
  const cfgExample = path.join(root, 'config.example.yaml');
  const dataDir = path.join(root, 'data');
  backendStartedAt = Date.now();
  backendExitedUnexpectedly = false;

  if (!fs.existsSync(exe)) throw new Error('cyberstrike-ai.exe 不存在，请重新安装或从 Release 下载完整包。');
  if (!fs.existsSync(cfg) && fs.existsSync(cfgExample)) fs.copyFileSync(cfgExample, cfg);
  if (!fs.existsSync(dataDir)) fs.mkdirSync(dataDir, { recursive: true });
  // 桌面版强制 local_mode=true 免登录 + 绑定 127.0.0.1，确保双击即用、不暴露公网
  ai.ensureDesktopDefaults(cfg);

  const env = Object.assign({}, process.env);
  env.PATH = pyHome + path.delimiter + path.join(pyHome, 'Scripts') + path.delimiter + (env.PATH || '');
  env.CYBERSTRIKE_HTTPS = '0';
  // 桌面壳自己打开 BrowserWindow，抑制后端自动开浏览器，避免双开
  env.CYBERSTRIKE_NO_AUTO_OPEN = '1';

  backendProc = spawn(exe, ['-config', cfg, '--http'], { cwd: root, env, windowsHide: false });
  // 把后端 stdout/stderr 落盘到 data/logs/desktop-backend.log，便于启动失败排查
  const fs2 = require('fs');
  const logDir = path.join(root, 'data', 'logs');
  try { fs2.mkdirSync(logDir, { recursive: true }); } catch {}
  const backendLog = path.join(logDir, 'desktop-backend.log');
  try {
    const out = fs2.createWriteStream(backendLog, { flags: 'a' });
    backendProc.stdout.pipe(out);
    backendProc.stderr.pipe(out);
  } catch {}
  backendProc.on('exit', (code) => {
    // 进程退出时若主窗口已关闭则不影响；若未关闭，可在此提示
    if (code !== 0 && code !== null && !backendExitDialogShown) {
      backendExitedUnexpectedly = true;
      // 启动后 30 秒内异常退出才弹原生错误框（避免正常退出/重启误弹）
      if (Date.now() - backendStartedAt < 30000 && mainWindow && !mainWindow.isDestroyed()) {
        backendExitDialogShown = true;
        dialog.showErrorBox('后端服务异常退出',
          '详情见 data/logs/desktop-backend.log，可从托盘菜单"重启后端"恢复。');
      }
    }
  });
}

function waitForOnline(port = 8080, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolve, reject) => {
    const tryConnect = () => {
      const sock = net.connect(port, '127.0.0.1');
      sock.setTimeout(1500);
      sock.on('connect', () => { sock.destroy(); resolve(); });
      sock.on('error', () => { if (Date.now() > deadline) reject(new Error('后端启动超时')); else setTimeout(tryConnect, 800); });
      sock.on('timeout', () => { sock.destroy(); if (Date.now() > deadline) reject(new Error('后端启动超时')); else setTimeout(tryConnect, 800); });
    };
    tryConnect();
  });
}

// ---- 配置窗口 ----
function createConfigWindow() {
  configWindow = new BrowserWindow({
    width: 620, height: 720, resizable: false,
    title: 'CyberStrikeAI · 配置 AI 通道',
    icon: iconPath(), autoHideMenuBar: true,
    webPreferences: {
      contextIsolation: true, nodeIntegration: false,
      preload: path.join(__dirname, 'config-preload.js')
    }
  });
  Menu.setApplicationMenu(null);
  configWindow.loadFile(path.join(__dirname, 'config.html'));
}

// ---- 主窗口 ----
async function createMainWindow() {
  mainWindow = new BrowserWindow({
    width: 1440, height: 900, minWidth: 1024, minHeight: 700,
    title: 'CyberStrikeAI', icon: iconPath(),
    backgroundColor: '#0b0f14', autoHideMenuBar: false,
    webPreferences: { contextIsolation: true, nodeIntegration: false }
  });
  // 主窗关闭 = 最小化到托盘（有托盘时），而非退出
  mainWindow.on('close', (e) => {
    if (tray.hasTray()) {
      e.preventDefault();
      if (mainWindow && !mainWindow.isDestroyed()) {
        mainWindow.hide();
        tray.showBalloon('CyberStrikeAI', '已最小化到托盘，点击托盘图标恢复。');
      }
    }
  });
  // 原生应用菜单：保留「在浏览器中打开」「重新加载」「开发者工具」「退出」等桌面原生入口
  const webURL = 'http://127.0.0.1:8080/';
  const template = [
    {
      label: 'CyberStrikeAI',
      submenu: [
        { label: '在浏览器中打开', click: () => shell.openExternal(webURL) },
        { type: 'separator' },
        { label: '退出', role: 'quit' }
      ]
    },
    {
      label: '视图',
      submenu: [
        { label: '重新加载', role: 'reload' },
        { label: '强制重新加载', role: 'forceReload' },
        { label: '开发者工具', role: 'toggleDevTools' },
        { type: 'separator' },
        { label: '放大', role: 'zoomIn' },
        { label: '缩小', role: 'zoomOut' },
        { label: '重置缩放', role: 'resetZoom' },
        { type: 'separator' },
        { label: '全屏', role: 'togglefullscreen' }
      ]
    }
  ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
  await mainWindow.loadURL(webURL);
  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith('http://127.0.0.1:8080') || url.startsWith('http://localhost:8080')) return { action: 'allow' };
    shell.openExternal(url); return { action: 'deny' };
  });
}

// ---- IPC：测试连接 / 保存并启动 ----
function setupIPC() {
  ipcMain.handle('ai:testConnection', async (_e, payload) => {
    const { provider, base_url, api_key, model } = payload;
    const headers = { 'Content-Type': 'application/json' };
    if (provider === 'claude') {
      // Claude 走 Anthropic Messages API /v1/messages
      headers['x-api-key'] = api_key;
      headers['anthropic-version'] = '2023-06-01';
      const url = base_url.replace(/\/+$/, '').replace(/\/v1\/messages$/, '') + '/v1/messages';
      try {
        const res = await fetch(url, {
          method: 'POST', headers,
          body: JSON.stringify({ model, max_tokens: 1, messages: [{ role: 'user', content: 'ping' }] }),
          signal: AbortSignal.timeout(15000)
        });
        if (res.ok || res.status === 400 || res.status === 409 || res.status === 429) return { ok: true, model };
        const txt = await res.text().catch(() => '');
        return { ok: false, error: 'HTTP ' + res.status + (txt ? ' · ' + txt.slice(0, 120) : '') };
      } catch (e) { return { ok: false, error: e.message }; }
    }
    // OpenAI 兼容：POST /chat/completions
    const url = base_url.replace(/\/+$/, '') + (base_url.endsWith('/chat/completions') ? '' : '/chat/completions');
    headers['Authorization'] = 'Bearer ' + api_key;
    try {
      const res = await fetch(url, {
        method: 'POST', headers,
        body: JSON.stringify({ model, messages: [{ role: 'user', content: 'ping' }], max_tokens: 1, stream: false }),
        signal: AbortSignal.timeout(15000)
      });
      if (res.ok || res.status === 400 || res.status === 409 || res.status === 429) {
        return { ok: true, model };
      }
      const txt = await res.text().catch(() => '');
      return { ok: false, error: 'HTTP ' + res.status + (txt ? ' · ' + txt.slice(0, 120) : '') };
    } catch (e) {
      return { ok: false, error: e.message };
    }
  });

  ipcMain.handle('ai:saveAndLaunch', async (_e, payload) => {
    try {
      ai.applyChannel(cfgPath(), {
        id: 'desktop', name: 'Desktop Channel',
        provider: payload.provider, base_url: payload.base_url,
        api_key: payload.api_key, model: payload.model,
        max_total_tokens: payload.max_total_tokens,
        max_completion_tokens: payload.max_completion_tokens
      });
      // 关闭配置窗口，启动后端 + 主窗口
      if (configWindow && !configWindow.isDestroyed()) configWindow.close();
      startBackend();
      await waitForOnline();
      await createMainWindow();
      return { ok: true };
    } catch (e) {
      return { ok: false, error: e.message || String(e) };
    }
  });

  ipcMain.handle('ext:openExternal', async (_e, url) => { shell.openExternal(url); });
}

// ---- 启动画面 ----
function createSplash() {
  splashWindow = new BrowserWindow({
    width: 600, height: 400, frame: false, resizable: false,
    transparent: true, alwaysOnTop: true, skipTaskbar: true,
    icon: iconPath(), backgroundColor: '#00000000'
  });
  splashWindow.loadFile(path.join(__dirname, 'splash.html'));
  splashWindow.on('closed', () => { splashWindow = null; });
}

function updateSplashStatus(text) {
  if (splashWindow && !splashWindow.isDestroyed()) {
    try { splashWindow.webContents.executeJavaScript(`updateStatus(${JSON.stringify(text)})`, true); } catch {}
  }
}

function closeSplash() {
  if (splashWindow && !splashWindow.isDestroyed()) {
    // 淡出后关闭：简单实现直接 destroy
    try { splashWindow.close(); } catch {}
    splashWindow = null;
  }
}

app.whenReady().then(async () => {
  setupIPC();

  // 单实例锁：防双开导致 8080 端口冲突
  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  app.on('second-instance', () => {
    // 已在运行：恢复并聚焦主窗/配置窗
    if (mainWindow && !mainWindow.isDestroyed()) {
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.show();
      mainWindow.focus();
    } else if (configWindow && !configWindow.isDestroyed()) {
      configWindow.show();
      configWindow.focus();
    }
  });

  // 启动画面先行
  createSplash();
  updateSplashStatus('启动后端服务…');

  // 检查 AI 通道是否已配置
  const cfg = ai.loadConfig(cfgPath());
  const { needSetup } = cfg ? ai.inspectAIChannel(cfg) : { needSetup: true };

  if (needSetup) {
    updateSplashStatus('检测 AI 通道配置…');
    closeSplash();
    createConfigWindow();
    return;
  }

  // 已配置 → 直接启动
  try {
    startBackend();
    updateSplashStatus('等待服务就绪…');
    await waitForOnline();
    updateSplashStatus('加载界面…');
    await createMainWindow();
    // 托盘常驻（关窗最小化到托盘，不退出）
    tray.createTray({
      getMainWindow: () => mainWindow,
      getConfigWindow: () => configWindow,
      restartBackend: async () => { try { startBackend(); await waitForOnline(); if (mainWindow) mainWindow.reload(); return { ok: true }; } catch (e) { return { ok: false, error: e.message }; } },
      quitApp: () => app.quit()
    });
    closeSplash();
  } catch (e) {
    closeSplash();
    const choice = dialog.showMessageBoxSync({
      type: 'error', title: 'CyberStrikeAI 启动失败',
      buttons: ['重试启动', '打开日志目录', '退出'], defaultId: 0, cancelId: 2,
      message: '后端启动超时或失败', detail: e.message + '\n\n可查看 data/logs/desktop-backend.log 排查。'
    });
    if (choice === 0) { startBackend(); }
    else if (choice === 1) { shell.openPath(path.join(rootDir(), 'data', 'logs')); }
    app.quit();
  }
});

app.on('window-all-closed', () => {
  // 有托盘时关窗最小化到托盘，不退出（macOS 同样行为）
  if (tray.hasTray()) {
    // 不退出，主窗已关；托盘菜单可重新显示
    return;
  }
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  tray.destroyTray();
  if (backendProc) {
    try { backendProc.kill(); } catch {}
    try { require('child_process').execSync('taskkill /F /IM cyberstrike-ai.exe', { stdio: 'ignore' }); } catch {}
  }
});
