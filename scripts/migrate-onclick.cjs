// onclick → data-action 迁移器（F4 CSP nonce 收紧前置）
// 把 index.html 的 onclick="fnName('arg1','arg2')" 迁移为
//   data-action="fnName" data-arg0="arg1" data-arg1="arg2"
// nav-delegate.js 的通用分发器读取 data-action + data-argN 调用对应全局函数。
//
// 处理范围：
//   - 简单单函数调用（460 处）：onclick="fn('a','b')" → data-action="fn" data-argN
//   - event 参数（5 处）：onclick="fn(event)" → data-action="fn" data-pass-event="1"
//   - this 参数（2 处）：onclick="fn(this)" → data-action="fn" data-pass-this="1"
//   - 多语句（9 处）：onclick="a();b('x')" → data-action-chain="a|b" data-arg-b="x"（委托器顺序调用）
//   - 条件调用（13 处）：onclick="if(cond)fn()" → data-action-if="cond" data-action="fn"
//   - 短路调用（1 处）：onclick="fn && fn()" → data-action="fn" data-optional="1"
//
// 不迁移（保留 inline + 同时 CSP 加 'unsafe-hashes' 兜底）：无——本脚本目标 100% 迁移。
//
// 用法：node scripts/chat-split/migrate-onclick.cjs
// 验证：迁移后 grep -c 'onclick=' web/templates/index.html 应为 0
const fs = require('fs');
const path = require('path');

const indexPath = path.resolve(process.cwd(), 'web', 'templates', 'index.html');
let html = fs.readFileSync(indexPath, 'utf8');

let migrated = 0, skipped = 0;
const actionRegistry = new Set();

// 匹配 onclick="..."
html = html.replace(/onclick="([^"]*)"/g, (match, body) => {
  const orig = body;
  body = body.trim();

  // 短路调用：fn && fn() 或 fn?.()
  const shortCircuit = body.match(/^([a-zA-Z_$][\w.$]*)\s*&&\s*\1\s*\(\s*\)\s*$/);
  if (shortCircuit) {
    const fn = shortCircuit[1].replace(/^window\./, '');
    actionRegistry.add(fn);
    migrated++;
    return `data-action="${fn}" data-optional="1"`;
  }

  // 多语句：a(); b('x'); event.stopPropagation();
  if (body.includes(';')) {
    // 拆分语句，去掉 event.stopPropagation()（委托器自动 stopPropagation）
    const stmts = body.split(';').map(s => s.trim()).filter(s => s);
    const filtered = stmts.filter(s => !/^event\.stopPropagation\s*\(\s*\)$/.test(s));
    if (filtered.length === 0) {
      // 全是 stopPropagation → 用 data-action="stopPropagation"
      actionRegistry.add('stopPropagation');
      migrated++;
      return `data-action="stopPropagation"`;
    }
    // 链式：每个语句必须是单函数调用
    const chain = [];
    let extraArgs = '';
    let argIdx = 0;
    let allSimple = true;
    for (const stmt of filtered) {
      const m = stmt.match(/^([a-zA-Z_$][\w.$]*)\s*\((.*)\)\s*$/);
      if (!m) { allSimple = false; break; }
      const fn = m[1].replace(/^window\./, '');
      const args = m[2];
      // 处理参数（只支持字面量 + event + this）
      const parsed = parseArgs(args);
      if (!parsed.ok) { allSimple = false; break; }
      chain.push(fn);
      actionRegistry.add(fn);
      parsed.args.forEach(a => {
        if (a.kind === 'literal') {
          extraArgs += ` data-arg${argIdx}="${escapeAttr(a.value)}"`;
          argIdx++;
        } else if (a.kind === 'event') {
          extraArgs += ` data-pass-event="${chain.length - 1}"`;
        } else if (a.kind === 'this') {
          extraArgs += ` data-pass-this="${chain.length - 1}"`;
        }
      });
    }
    if (allSimple && chain.length > 0) {
      migrated++;
      return `data-action-chain="${chain.join('|')}"${extraArgs ? extraArgs : ''}`;
    }
    // 复杂多语句无法自动迁移 → 保留并记录
    skipped++;
    console.error('SKIP multi-stmt: ' + orig.slice(0, 80));
    return match;
  }

  // 单函数调用
  const single = body.match(/^([a-zA-Z_$][\w.$]*)\s*\((.*)\)\s*$/);
  if (single) {
    const fn = single[1].replace(/^window\./, '');
    const args = single[2];
    const parsed = parseArgs(args);
    if (parsed.ok) {
      actionRegistry.add(fn);
      let attrs = `data-action="${fn}"`;
      let argIdx = 0;
      parsed.args.forEach(a => {
        if (a.kind === 'literal') {
          attrs += ` data-arg${argIdx}="${escapeAttr(a.value)}"`;
          argIdx++;
        } else if (a.kind === 'event') {
          attrs += ` data-pass-event="1"`;
        } else if (a.kind === 'this') {
          attrs += ` data-pass-this="1"`;
        }
      });
      migrated++;
      return attrs;
    }
    // 参数含变量（非 event/this）→ 无法迁移
    skipped++;
    console.error('SKIP ident-arg: ' + orig.slice(0, 80));
    return match;
  }

  // 条件调用：if(cond)fn() 或 if(event.target===this)closeXxx()
  const condMatch = body.match(/^if\s*\(([^)]*)\)\s*([a-zA-Z_$][\w.$]*\s*\([^)]*\))\s*$/);
  if (condMatch) {
    const cond = condMatch[1].trim();
    const call = condMatch[2].trim();
    const cm = call.match(/^([a-zA-Z_$][\w.$]*)\s*\((.*)\)\s*$/);
    if (cm) {
      const fn = cm[1].replace(/^window\./, '');
      actionRegistry.add(fn);
      const condAttr = cond === "event.target===this" ? 'data-if-self="1"' : `data-if-cond="${escapeAttr(cond)}"`;
      migrated++;
      return `data-action="${fn}" ${condAttr} data-pass-event="1"`;
    }
  }

  skipped++;
  console.error('SKIP unknown: ' + orig.slice(0, 80));
  return match;
});

function parseArgs(argsStr) {
  const args = [];
  const trimmed = argsStr.trim();
  if (trimmed === '') return { ok: true, args };
  // 简单分割（不处理嵌套逗号，但本项目参数都是简单字面量）
  const parts = splitArgs(trimmed);
  for (const p of parts) {
    const t = p.trim();
    if (t === 'event') { args.push({ kind: 'event' }); continue; }
    if (t === 'this') { args.push({ kind: 'this' }); continue; }
    // 字符串字面量
    const sm = t.match(/^'(.*)'$/) || t.match(/^"(.*)"$/);
    if (sm) { args.push({ kind: 'literal', value: sm[1] }); continue; }
    // 数字
    if (/^-?\d+(\.\d+)?$/.test(t)) { args.push({ kind: 'literal', value: t }); continue; }
    // 布尔/null
    if (/^(true|false|null|undefined)$/.test(t)) { args.push({ kind: 'literal', value: t }); continue; }
    // 标识符（动态值）→ 无法迁移
    return { ok: false, args };
  }
  return { ok: true, args };
}

function splitArgs(s) {
  // 简单逗号分割（不处理括号嵌套，本项目参数无嵌套）
  const out = [];
  let depth = 0, cur = '';
  for (const ch of s) {
    if (ch === '(' || ch === '[' || ch === '{') depth++;
    if (ch === ')' || ch === ']' || ch === '}') depth--;
    if (ch === ',' && depth === 0) { out.push(cur); cur = ''; continue; }
    cur += ch;
  }
  if (cur.trim()) out.push(cur);
  return out;
}

function escapeAttr(v) {
  return String(v).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

fs.writeFileSync(indexPath, html, 'utf8');
console.log(`migrated: ${migrated}, skipped: ${skipped}`);
console.log(`unique actions: ${actionRegistry.size}`);
console.log('actions: ' + [...actionRegistry].slice(0, 30).join(', ') + (actionRegistry.size > 30 ? '...' : ''));
