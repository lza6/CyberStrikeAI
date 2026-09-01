// 系统托盘：常驻入口（显示主窗 / 打开 Web / 重启后端 / 退出）+ 最小化到托盘气泡
// 文案直接中文硬编码：桌面壳是中文产品，主进程不接 web 端 i18n 体系（避免为主进程
// 引入额外依赖与运行时），后续若需要多语言再统一抽取文案表。
const { Tray, Menu, shell, nativeImage, Notification } = require('electron');
const path = require('path');

const ICON_FILE = path.join(__dirname, '..', 'icons', 'icon.png');
const WEB_URL = 'http://127.0.0.1:8080';

let tray = null;

function buildIcon() {
  const img = nativeImage.createFromPath(ICON_FILE);
  if (img.isEmpty()) {
    // 兜底：icons/icon.png 缺失时退回 Electron 空图标（托盘槽位可能显示空白）。
    // 发布包由 electron-builder 携带 icons 目录，正常不会走到这里。
    return nativeImage.createEmpty();
  }
  return img;
}

// 托盘气泡：Windows 用原生 displayBalloon；其他平台退回 Electron Notification
function showBalloon(title, content) {
  if (!tray || tray.isDestroyed()) return;
  if (process.platform === 'win32' && typeof tray.displayBalloon === 'function') {
    try { tray.displayBalloon({ title, content }); } catch {}
    return;
  }
  try { new Notification({ title, body: content }).show(); } catch {}
}

function showMainWindow(getMainWindow) {
  const win = getMainWindow();
  if (win && !win.isDestroyed()) {
    if (win.isMinimized()) win.restore();
    win.show();
    win.focus();
    return true;
  }
  return false;
}

// deps 由 main.js 注入（getMainWindow / getConfigWindow / restartBackend / quitApp），
// tray.js 不持有业务状态，避免与 main.js 循环依赖。
function createTray(deps) {
  if (tray) return tray;
  tray = new Tray(buildIcon());
  tray.setToolTip('CyberStrikeAI · 智能渗透平台');

  const menu = Menu.buildFromTemplate([
    {
      label: '显示主窗口',
      click: () => {
        if (!showMainWindow(deps.getMainWindow)) {
          // 主窗尚未创建（如仍在配置阶段）时，聚焦配置窗兜底
          const cfg = deps.getConfigWindow && deps.getConfigWindow();
          if (cfg && !cfg.isDestroyed()) { cfg.show(); cfg.focus(); }
        }
      }
    },
    { label: '打开 Web 界面', click: () => { shell.openExternal(WEB_URL); } },
    {
      label: '重启后端',
      click: async () => {
        showBalloon('CyberStrikeAI', '后端重启中，请稍候…');
        try {
          const r = await deps.restartBackend();
          showBalloon('CyberStrikeAI',
            r && r.ok ? '后端已重启，主窗口已刷新。' : '后端重启失败：' + ((r && r.error) || '未知错误'));
        } catch (e) {
          showBalloon('CyberStrikeAI', '后端重启失败：' + ((e && e.message) || e));
        }
      }
    },
    { type: 'separator' },
    { label: '退出', click: () => { deps.quitApp(); } }
  ]);
  tray.setContextMenu(menu);
  // 双击托盘图标 = 显示主窗口（Windows 用户习惯）
  tray.on('double-click', () => { showMainWindow(deps.getMainWindow); });
  return tray;
}

function hasTray() { return !!tray && !tray.isDestroyed(); }

function destroyTray() {
  if (tray && !tray.isDestroyed()) tray.destroy();
  tray = null;
}

module.exports = { createTray, hasTray, destroyTray, showBalloon };
