// J10 chat.js 拆分等价性 + node --check 回归校验（可重复运行）
// 用法：node scripts/chat-split/verify.cjs
// 通过条件：
//   1) 10 段按切点拼装 === 原 chat.js（SHA256 比对）
//   2) 10 段全部 node --check 通过
//   3) index.html 引用了全部 10 段且未再引用旧 chat.js
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { execSync } = require('child_process');

const repoRoot = path.resolve(__dirname, '..', '..');
const srcPath = path.join(repoRoot, 'web', 'static', 'js', 'chat.js');
const destDir = path.join(repoRoot, 'web', 'static', 'js', 'chat');
const indexPath = path.join(repoRoot, 'web', 'templates', 'index.html');

const segments = [
  { name: 'chat-core.js',                 start: 1,    end: 921   },
  { name: 'input.js',                     start: 922,  end: 2104  },
  { name: 'render.js',                    start: 2105, end: 3342  },
  { name: 'tools.js',                     start: 3343, end: 4610  },
  { name: 'input-binding.js',             start: 4611, end: 5401  },
  { name: 'mcp-detail.js',                start: 5402, end: 5858  },
  { name: 'history.js',                    start: 5859, end: 6734  },
  { name: 'attack-chain.js',              start: 6735, end: 8028  },
  { name: 'conversations.js',            start: 8029, end: 9902  },
  { name: 'context-menu-batch-i18n.js',  start: 9903, end: 11236 },
];

let failures = 0;
function fail(msg) { console.error('  ✗ ' + msg); failures++; }
function ok(msg) { console.log('  ✓ ' + msg); }

console.log('=== J10 chat.js 拆分回归校验 ===');

// 1. 等价性
const text = fs.readFileSync(srcPath, 'utf8');
const sep = text.includes('\r\n') ? '\r\n' : '\n';
const lines = text.split(sep);
let assembled = '';
for (const seg of segments) {
  const slice = lines.slice(seg.start - 1, seg.end);
  const body = slice.join(sep);
  if (seg !== segments[segments.length - 1]) assembled += body + sep;
  else { assembled += body; if (text.endsWith(sep) && !assembled.endsWith(sep)) assembled += sep; }
}
const h1 = crypto.createHash('sha256').update(text).digest('hex');
const h2 = crypto.createHash('sha256').update(assembled).digest('hex');
if (h1 === h2) ok(`等价性：10 段拼装 === chat.js（SHA256 ${h1.slice(0,12)}…）`);
else fail(`等价性失败：原文 ${h1} ≠ 拼装 ${h2}`);

// 2. node --check 每段
let checkAllPass = true;
for (const seg of segments) {
  const p = path.join(destDir, seg.name);
  if (!fs.existsSync(p)) { fail(`缺失文件 ${seg.name}`); checkAllPass = false; continue; }
  try {
    execSync(`node --check "${p}"`, { stdio: 'pipe' });
    console.log('  ✓ node --check ' + seg.name);
  } catch (e) {
    fail('node --check ' + seg.name + '：' + (e.stderr ? e.stderr.toString().split('\n')[0] : e.message));
    checkAllPass = false;
  }
}
if (checkAllPass) ok('全部 10 段语法校验通过');

// 3. index.html 引用检查（版本号 ?v= 不限定具体值，缓存升级改版本不破坏校验）
const html = fs.readFileSync(indexPath, 'utf8');
let htmlOk = true;
for (const seg of segments) {
  const re = new RegExp('/static/js/chat/' + seg.name.replace(/\./g, '\\.') + '\\?v=[0-9-]+');
  if (!re.test(html)) { fail(`index.html 未引用 /static/js/chat/${seg.name}（?v= 任意版本）`); htmlOk = false; }
}
// 旧 chat.js 引用应已移除（注释里的不算）
const oldRefs = html.split('\n').filter(l => l.includes('/static/js/chat.js') && !l.trim().startsWith('//') && !l.includes('<!--'));
if (oldRefs.length > 0) { fail(`index.html 仍引用旧 chat.js：${oldRefs.length} 处`); htmlOk = false; }
else ok('index.html 已引用全部 10 段且移除旧 chat.js 引用');

console.log('');
if (failures === 0) { console.log('✅ J10 回归校验全部通过（等价 + 语法 + 引用）'); process.exit(0); }
else { console.error(`❌ J10 回归校验有 ${failures} 项失败`); process.exit(1); }
