// J10 chat.js 拆分切分器（byte-exact，行为不变）
// 用法：node scripts/chat-split/split.js
// 校验：concat(段1..段10) === 原 chat.js（SHA256 比对）
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const repoRoot = path.resolve(__dirname, '..', '..');
const srcPath = path.join(repoRoot, 'web', 'static', 'js', 'chat.js');
const destDir = path.join(repoRoot, 'web', 'static', 'js', 'chat');

// 切点定义（1-indexed，含两端，已验证全部落在 `}` + 空行 + 注释边界）
const segments = [
  { name: 'chat-core.js',                 start: 1,    end: 921   },
  { name: 'input.js',                     start: 922,  end: 2104  },
  { name: 'render.js',                    start: 2105, end: 3342  },
  { name: 'tools.js',                     start: 3343, end: 4610  },
  { name: 'input-binding.js',             start: 4611, end: 5401  },
  { name: 'mcp-detail.js',                start: 5402, end: 5858  },
  { name: 'history.js',                   start: 5859, end: 6734  },
  { name: 'attack-chain.js',              start: 6735, end: 8028  },
  { name: 'conversations.js',             start: 8029, end: 9902  },
  { name: 'context-menu-batch-i18n.js',   start: 9903, end: 11236 },
];

function main() {
  const raw = fs.readFileSync(srcPath); // Buffer，保留原始字节
  // 检测行分隔符
  const hasCRLF = raw.includes(Buffer.from('\r\n'));
  const sep = hasCRLF ? '\r\n' : '\n';
  const text = raw.toString('utf8');
  const lines = text.split(sep);
  // split 后 lines.length：若文件以 sep 结尾，最后一项是 ''（代表尾行后的空）
  // 行号 1-indexed：lines[0] = 行1
  const totalLines = lines.length;
  console.log(`[split] 源文件 ${totalLines} 行（split 后数组长度），换行符 = ${hasCRLF ? 'CRLF' : 'LF'}`);

  if (totalLines < 11188) {
    console.error(`[split] 警告：期望 ≥11188 行，实际 ${totalLines}，可能行号漂移，中止`);
    process.exit(2);
  }

  fs.mkdirSync(destDir, { recursive: true });

  let assembled = '';
  for (const seg of segments) {
    const slice = lines.slice(seg.start - 1, seg.end); // 含 end
    const body = slice.join(sep);
    // 段间拼接：每段末尾原本就是换行后的内容；为保证 concat 等价，段之间需有 sep
    // 原 chat.js 第 end 行之后紧跟第 end+1 行（下一段的 start），中间恰好一个 sep
    // 所以 assembled += body + sep（除了最后一段，因为原文件末尾的 sep 已在 lines 最后的 '' 中）
    const outPath = path.join(destDir, seg.name);
    fs.writeFileSync(outPath, body, 'utf8');
    const stat = fs.statSync(outPath);
    console.log(`[split] 写 ${seg.name}：行 ${seg.start}-${seg.end}（${slice.length} 行，${stat.size} 字节）`);
    if (seg !== segments[segments.length - 1]) {
      assembled += body + sep;
    } else {
      assembled += body;
      // 最后一段若原文件以换行结尾，assembled 需补尾换行
      if (text.endsWith(sep) && !assembled.endsWith(sep)) assembled += sep;
    }
  }

  // 等价性校验：assembled 必须与原 text 逐字节相等
  const origHash = crypto.createHash('sha256').update(text).digest('hex');
  const asmHash = crypto.createHash('sha256').update(assembled).digest('hex');
  console.log(`[split] 原文 SHA256 = ${origHash}`);
  console.log(`[split] 拼装 SHA256 = ${asmHash}`);
  if (origHash !== asmHash) {
    // 找首个差异
    const n = Math.min(text.length, assembled.length);
    let diffAt = -1;
    for (let i = 0; i < n; i++) {
      if (text[i] !== assembled[i]) { diffAt = i; break; }
    }
    if (diffAt < 0 && text.length !== assembled.length) {
      diffAt = n;
    }
    console.error(`[split] 等价性失败：首个差异 @${diffAt}（原文长 ${text.length}，拼装长 ${assembled.length}）`);
    console.error(`[split] 原文附近：${JSON.stringify(text.slice(Math.max(0, diffAt - 40), diffAt + 40))}`);
    console.error(`[split] 拼装附近：${JSON.stringify(assembled.slice(Math.max(0, diffAt - 40), diffAt + 40))}`);
    process.exit(3);
  }
  console.log('[split] 等价性校验通过：10 段拼装 === 原 chat.js（逐字节）');

  // 输出切点摘要供 index.html 引用
  console.log('\n[split] index.html 应替换为：');
  for (const seg of segments) {
    console.log(`    <script src="/static/js/chat/${seg.name}?v=20260902-1"></script>`);
  }
}

main();
