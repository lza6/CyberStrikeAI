// smoke 测试：桌面壳关键文件存在性 + 关键函数引用（不依赖 electron 运行时）
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const src = path.join(__dirname, '..', 'src');
let failures = 0;

function check(cond, msg) {
  if (cond) { console.log('  ✓', msg); }
  else { console.error('  ✗', msg); failures++; }
}

console.log('I4 桌面外壳 smoke 测试');

// 1. 关键文件存在
check(fs.existsSync(path.join(src, 'main.js')), 'main.js 存在');
check(fs.existsSync(path.join(src, 'tray.js')), 'tray.js 存在');
check(fs.existsSync(path.join(src, 'splash.html')), 'splash.html 存在');
check(fs.existsSync(path.join(src, 'ai-config.js')), 'ai-config.js 存在');

// 2. main.js 关键功能存在
const mainJs = fs.readFileSync(path.join(src, 'main.js'), 'utf8');
check(mainJs.includes('requestSingleInstanceLock'), '单实例锁');
check(mainJs.includes("second-instance"), '单实例 second-instance 恢复');
check(mainJs.includes('createSplash'), '启动画面函数');
check(mainJs.includes('updateSplashStatus'), 'splash 状态更新');
check(mainJs.includes('closeSplash'), 'splash 关闭');
check(mainJs.includes('createTray'), '托盘创建');
check(mainJs.includes('backendStartedAt'), '后端启动时间记录（异常退出判断用）');
check(mainJs.includes('tray.hasTray()'), '关窗最小化到托盘');
check(mainJs.includes('dialog.showMessageBoxSync'), '原生错误对话框');
check(mainJs.includes('shell.openPath'), '打开日志目录');

// 3. tray.js 关键功能
const trayJs = fs.readFileSync(path.join(src, 'tray.js'), 'utf8');
check(trayJs.includes('createTray'), 'createTray 导出');
check(trayJs.includes('destroyTray'), 'destroyTray 导出');
check(trayJs.includes('showBalloon'), 'showBalloon 气泡');
check(trayJs.includes('显示主窗口'), '托盘菜单中文');
check(trayJs.includes('重启后端'), '托盘重启后端菜单');
check(trayJs.includes('退出'), '托盘退出菜单');

// 4. splash.html 必要元素
const splashHtml = fs.readFileSync(path.join(src, 'splash.html'), 'utf8');
check(splashHtml.includes('id="status"'), 'splash status 元素');
check(splashHtml.includes('updateStatus'), 'splash updateStatus 函数');
check(splashHtml.includes('#0b0f14'), 'splash 深色背景匹配主窗');

// 5. package.json main 入口未变
const pkg = JSON.parse(fs.readFileSync(path.join(src, '..', 'package.json'), 'utf8'));
check(pkg.main === 'src/main.js' || pkg.main === 'main.js', 'package.json main 入口');

if (failures > 0) {
  console.error(`\n失败 ${failures} 项`);
  process.exit(1);
}
console.log('\n全部通过');
