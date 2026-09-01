// CyberStrikeAI 桌面启动器主进程
// 职责：
//   1. 启动前检查 AI 通道是否已配置（否则弹出配置窗口引导小白填 Key）
//   2. 拉起内嵌后端 cyberstrike-ai.exe，等待 ONLINE
//   3. 打开主 BrowserWindow 指向 http://127.0.0.1:8080
const { app, BrowserWindow, shell, Menu, ipcMain } = require('electron');
const path = require('path');
const fs = require('fs');
const net = require('net');
const { spawn } = require('child_process');
const ai = require('./ai-config');

let backendProc = null;
let mainWindow = null;
let configWindow = null;

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

app.whenReady().then(async () => {
  setupIPC();

  // 检查 AI 通道是否已配置
  const cfg = ai.loadConfig(cfgPath());
  const { needSetup } = cfg ? ai.inspectAIChannel(cfg) : { needSetup: true };

  if (needSetup) {
    createConfigWindow();
    return;
  }

  // 已配置 → 直接启动
  try {
    startBackend();
    await waitForOnline();
    await createMainWindow();
  } catch (e) {
    const { dialog } = require('electron');
    dialog.showErrorBox('CyberStrikeAI 启动失败', e.message);
    app.quit();
  }
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  if (backendProc) {
    try { backendProc.kill(); } catch {}
    try { require('child_process').execSync('taskkill /F /IM cyberstrike-ai.exe', { stdio: 'ignore' }); } catch {}
  }
});
