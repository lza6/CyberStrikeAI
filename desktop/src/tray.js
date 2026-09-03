// 系统托盘：常驻入口（显示主窗 / 打开 Web / 重启后端 / 退出）+ 最小化到托盘气泡
// 文案直接中文硬编码：桌面壳是中文产品，主进程不接 web 端 i18n 体系（避免为主进程
// 引入额外依赖与运行时），后续若需要多语言再统一抽取文案表。
const { Tray, Menu, shell, nativeImage, Notification } = require('electron');
const path = require('path');

const ICON_FILE = path.join(__dirname, '..', 'icons', 'icon.png');
const WEB_URL = 'http://127.0.0.1:8080';

// K4 黑匣子打开 · 托盘动态状态色
// 轮询 /api/tasks/active（活跃任务）+ /api/hitl/pending（待审批），tooltip 显示
// "N 任务运行 · M 待审批"，图标叠加色点：绿=健康 / 黄=有待审批 / 红=后端异常。
// 跨平台：Win 用 ico 叠加、macOS 用 png template image、Linux 用 AppIndicator 兜底文本。
const DOT_GREEN = path.join(__dirname, '..', 'icons', 'dot-green.png');
const DOT_YELLOW = path.join(__dirname, '..', 'icons', 'dot-yellow.png');
const DOT_RED = path.join(__dirname, '..', 'icons', 'dot-red.png');
const STATUS_POLL_INTERVAL_MS = 5000;
const STATUS_FETCH_TIMEOUT_MS = 3000;

let tray = null;
let statusTimer = null;
let lastStatus = { state: 'starting', runningTasks: 0, pendingApprovals: 0 };
// 记录初始图标（各平台一份干净底图），色点叠加在其上重绘
let baseIcon = null;

function buildIcon() {
  const img = nativeImage.createFromPath(ICON_FILE);
  if (img.isEmpty()) {
    // 兜底：icons/icon.png 缺失时退回 Electron 空图标（托盘槽位可能显示空白）。
    // 发布包由 electron-builder 携带 icons 目录，正常不会走到这里。
    return nativeImage.createEmpty();
  }
  return img;
}

// ---- 跨平台色点叠加 ----
// macOS: template image（黑 alpha 轮廓）由系统自动渲染，色点直接叠加在右下角。
// Windows: 用 addRepresentation 在 icon 画布右下角画一个实心色点。
// Linux (AppIndicator): 不支持运行时改图，降级为纯文本 tooltip 描述状态。
function buildStatusIcon(state) {
  const base = baseIcon || (baseIcon = buildIcon());
  if (base.isEmpty()) return base;
  if (process.platform === 'linux') {
    // AppIndicator 兜底：不叠色点，仅靠 tooltip/菜单文本描述状态
    return base;
  }
  const dotFile = state === 'healthy' ? DOT_GREEN
    : (state === 'pending' ? DOT_YELLOW
      : (state === 'error' ? DOT_RED : null));
  if (!dotFile) return base;
  const dot = nativeImage.createFromPath(dotFile);
  if (dot.isEmpty()) return base;
  // 叠加：把 16x16 色点合成到 base 右下角（新建 composite 图，不改原 base 缓存）
  try {
    const bw = base.getSize().width;
    const bh = base.getSize().height;
    const dw = Math.max(8, Math.round(bw * 0.35));
    const dh = Math.max(8, Math.round(bh * 0.35));
    const dotResized = dot.resize({ width: dw, height: dh });
    const out = nativeImage.createEmpty();
    // base 全幅
    out.addRepresentation({
      scaleFactor: 1.0,
      width: bw,
      height: bh,
      buffer: base.toBitmap()
    });
    // 色点右下角
    out.addRepresentation({
      scaleFactor: 1.0,
      width: dw,
      height: dh,
      buffer: dotResized.toBitmap(),
      offsetX: Math.max(0, bw - dw),
      offsetY: Math.max(0, bh - dh)
    });
    if (process.platform === 'darwin') {
      // macOS 菜单栏图标建议 template（系统自适应深浅色）；色点以彩色保留，
      // 不标 template，否则色点会被单色化。
      out.setTemplateImage(false);
    }
    return out;
  } catch (e) {
    return base;
  }
}

// ---- 动态 tooltip ----
function statusTooltipText(st) {
  const running = Number(st.runningTasks || 0);
  const pending = Number(st.pendingApprovals || 0);
  let line;
  if (st.state === 'error') line = '后端异常';
  else if (st.state === 'starting') line = '后端启动中…';
  else if (st.state === 'pending') line = running + ' 任务运行 · ' + pending + ' 待审批';
  else line = running > 0 ? (running + ' 任务运行') : '健康';
  return 'CyberStrikeAI · 智能渗透平台\n' + line;
}

function applyTrayStatus(st) {
  lastStatus = st;
  if (!tray || tray.isDestroyed()) return;
  try { tray.setToolTip(statusTooltipText(st)); } catch {}
  try { tray.setImage(buildStatusIcon(st.state)); } catch {}
  // Linux AppIndicator：托盘标题（部分环境可见）+ 菜单首行兜底文本
  if (process.platform === 'linux' && typeof tray.setTitle === 'function') {
    try { tray.setTitle(statusTooltipText(st).split('\n')[1] || ''); } catch {}
  }
  refreshStatusMenu();
}

function refreshStatusMenu() {
  if (!tray || tray.isDestroyed()) return;
  const st = lastStatus;
  const statusLabel = st.state === 'error'
    ? '● 状态：后端异常'
    : (st.state === 'starting'
      ? '● 状态：启动中'
      : (st.state === 'pending'
        ? '● 状态：' + Number(st.runningTasks || 0) + ' 任务运行 · ' + Number(st.pendingApprovals || 0) + ' 待审批'
        : '● 状态：健康'));
  try {
    tray.setContextMenu(Menu.buildFromTemplate([
      { label: statusLabel, enabled: false },
      { type: 'separator' },
      {
        label: '显示主窗口',
        click: () => {
          if (!showMainWindow(depsRef.getMainWindow)) {
            const cfg = depsRef.getConfigWindow && depsRef.getConfigWindow();
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
            const r = await depsRef.restartBackend();
            showBalloon('CyberStrikeAI',
              r && r.ok ? '后端已重启，主窗口已刷新。' : '后端重启失败：' + ((r && r.error) || '未知错误'));
          } catch (e) {
            showBalloon('CyberStrikeAI', '后端重启失败：' + ((e && e.message) || e));
          }
        }
      },
      { type: 'separator' },
      { label: '退出', click: () => { depsRef.quitApp(); } }
    ]));
  } catch (e) { /* 菜单重建失败保留旧菜单 */ }
}

// main.js 注入的 deps 引用（createTray 期间赋值，供轮询/菜单重建用）
let depsRef = { getMainWindow: () => null, getConfigWindow: () => null, restartBackend: async () => ({ ok: false }), quitApp: () => {} };

// ---- 状态轮询 ----
async function fetchJsonWithTimeout(url, timeoutMs) {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(timeoutMs || STATUS_FETCH_TIMEOUT_MS) });
    if (!res.ok) return null;
    return await res.json();
  } catch (e) {
    return null;
  }
}

async function pollBackendStatus() {
  if (!tray || tray.isDestroyed()) return;
  // 先按 main.js waitForOnline 同款端口语义探活：任一接口失败两次以上视为异常
  const [tasks, hitl] = await Promise.all([
    fetchJsonWithTimeout(WEB_URL + '/api/agent-loop/tasks'),
    fetchJsonWithTimeout(WEB_URL + '/api/hitl/pending?page=1&pageSize=200')
  ]);
  let nextState;
  let runningTasks = 0;
  let pendingApprovals = 0;
  if (tasks === null && hitl === null) {
    nextState = 'error';
  } else {
    if (tasks && Array.isArray(tasks.tasks)) {
      runningTasks = tasks.tasks.filter((t) => t && (t.status === 'running' || t.status === 'cancelling')).length;
    }
    if (hitl && Array.isArray(hitl.items)) {
      // 只统计人审 pending（audit_running 由 Audit Agent 自动消化，不算人工等待）
      pendingApprovals = hitl.items.filter((it) => {
        const reviewer = String((it && (it.reviewer || it.decidedBy || it.decided_by)) || '').toLowerCase();
        const status = String((it && it.status) || '').toLowerCase();
        return reviewer !== 'audit_agent' && status !== 'audit_running';
      }).length;
    }
    nextState = pendingApprovals > 0 ? 'pending' : 'healthy';
  }
  const changed = nextState !== lastStatus.state ||
    runningTasks !== Number(lastStatus.runningTasks || 0) ||
    pendingApprovals !== Number(lastStatus.pendingApprovals || 0);
  if (changed) {
    applyTrayStatus({ state: nextState, runningTasks, pendingApprovals });
  }
}

function startStatusPolling() {
  if (statusTimer) return;
  statusTimer = setInterval(() => {
    pollBackendStatus().catch(() => {});
  }, STATUS_POLL_INTERVAL_MS);
  // 启动后立即拉一次
  pollBackendStatus().catch(() => {});
}

function stopStatusPolling() {
  if (statusTimer) {
    clearInterval(statusTimer);
    statusTimer = null;
  }
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
  depsRef = deps || depsRef;
  baseIcon = buildIcon();
  tray = new Tray(baseIcon);
  tray.setToolTip('CyberStrikeAI · 智能渗透平台');

  refreshStatusMenu();
  // 双击托盘图标 = 显示主窗口（Windows 用户习惯）
  tray.on('double-click', () => { showMainWindow(depsRef.getMainWindow); });
  // K4：启动动态状态轮询（tooltip + 色点）
  startStatusPolling();
  return tray;
}

function hasTray() { return !!tray && !tray.isDestroyed(); }

function destroyTray() {
  stopStatusPolling();
  if (tray && !tray.isDestroyed()) tray.destroy();
  tray = null;
}

module.exports = { createTray, hasTray, destroyTray, showBalloon, pollBackendStatus, applyTrayStatus };
