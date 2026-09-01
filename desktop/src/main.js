// CyberStrikeAI 桌面启动器主进程
// 职责：定位/启动内嵌的后端二进制，等待 ONLINE，打开内嵌 BrowserWindow 指向 http://127.0.0.1:8080
const { app, BrowserWindow, shell, Menu } = require('electron');
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');

let backendProc = null;
let mainWindow = null;

// 生产环境：app.isPackaged 时资源在 process.resourcesPath；开发时用项目根
function rootDir() {
  if (app.isPackaged) return process.resourcesPath;
  return path.join(__dirname, '..', '..');
}

function startBackend() {
  const root = rootDir();
  const exe = path.join(root, 'cyberstrike-ai.exe');
  const pyHome = path.join(root, 'runtime', 'python', 'python-3.13.5');
  const cfg = path.join(root, 'config.yaml');
  const cfgExample = path.join(root, 'config.example.yaml');
  const dataDir = path.join(root, 'data');

  if (!fs.existsSync(exe)) {
    throw new Error('cyberstrike-ai.exe 不存在，请重新安装或从 Release 下载完整包。');
  }
  if (!fs.existsSync(cfg) && fs.existsSync(cfgExample)) {
    fs.copyFileSync(cfgExample, cfg);
  }
  if (!fs.existsSync(dataDir)) fs.mkdirSync(dataDir, { recursive: true });

  const env = Object.assign({}, process.env);
  // 内嵌 Python 优先
  env.PATH = path.join(pyHome) + path.delimiter + path.join(pyHome, 'Scripts') + path.delimiter + (env.PATH || '');
  env.CYBERSTRIKE_HTTPS = '0';

  const args = ['-config', cfg, '--http'];
  backendProc = spawn(exe, args, { cwd: root, env, windowsHide: false, detached: false });
  backendProc.stdout.on('data', () => {});
  backendProc.stderr.on('data', () => {});
  backendProc.on('exit', (code) => {
    if (mainWindow && !mainWindow.isDestroyed()) {
      // 进程意外退出时给用户提示
    }
  });
}

async function waitForOnline(port = 8080, timeoutMs = 60000) {
  const net = require('net');
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

async function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 1024,
    minHeight: 700,
    title: 'CyberStrikeAI',
    backgroundColor: '#0b0f14',
    autoHideMenuBar: true,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false
    }
  });

  // 隐藏菜单栏（小白不需要）
  Menu.setApplicationMenu(null);

  // 加载本地后端
  await mainWindow.loadURL('http://127.0.0.1:8080/');

  // 外链在系统浏览器打开
  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith('http://127.0.0.1:8080') || url.startsWith('http://localhost:8080')) {
      return { action: 'allow' };
    }
    shell.openExternal(url);
    return { action: 'deny' };
  });
}

app.whenReady().then(async () => {
  try {
    startBackend();
  } catch (e) {
    const { dialog } = require('electron');
    dialog.showErrorBox('CyberStrikeAI 启动失败', e.message);
    app.quit();
    return;
  }
  try {
    await waitForOnline();
    await createWindow();
  } catch (e) {
    const { dialog } = require('electron');
    dialog.showErrorBox('CyberStrikeAI 启动失败', '后端未能在 60 秒内就绪：' + e.message);
    app.quit();
  }
}).catch(err => {
  console.error('whenReady error:', err);
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  if (backendProc) {
    try { backendProc.kill(); } catch {}
    // Windows 下子进程可能未随父退出，强制结束
    try {
      const { execSync } = require('child_process');
      execSync('taskkill /F /IM cyberstrike-ai.exe', { stdio: 'ignore' });
    } catch {}
  }
});
