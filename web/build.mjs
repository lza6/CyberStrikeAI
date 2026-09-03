// CyberStrikeAI 前端构建脚本（渐进增强：压缩 + 文件哈希 + 静态 gzip）
// 设计原则（遵循《结果计划指南.md》F1）：
//   - 不改 web/templates/index.html 的 50 个 <script> 顺序/事件/i18n（保持原始 raw 加载为默认）
//   - 仅产出压缩+哈希+gzip 产物到 web/static/dist/，供可选 prod 引用切换
//   - 全部用 Node 内置模块 + terser/clean-css，不引入 Vite dev server 重型依赖
//   - 可重复构建：内容不变则哈希不变
import { promises as fs } from 'node:fs';
import { createHash } from 'node:crypto';
import { gzip } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const WEB_ROOT = __dirname;
const STATIC = path.join(WEB_ROOT, 'static');
const DIST = path.join(STATIC, 'dist');
const CHECK_ONLY = process.argv.includes('--check');

// terser / clean-css 都装在 web/node_modules 下
let terserApi = null;
async function loadTerser() {
  if (terserApi) return terserApi;
  try {
    terserApi = await import('terser');
  } catch (e) {
    console.error('[build] terser 未安装，请先 npm install');
    throw e;
  }
  return terserApi;
}

let CleanCSSClass = null;
async function loadCleanCSS() {
  if (CleanCSSClass) return CleanCSSClass;
  try {
    const mod = await import('clean-css');
    CleanCSSClass = mod.default || mod;
  } catch (e) {
    console.warn('[build] clean-css 未安装，CSS 将保留原内容: ' + e.message);
    CleanCSSClass = null;
  }
  return CleanCSSClass;
}

async function gzipTo(buf) {
  return new Promise((res, rej) => gzip(buf, { level: 9 }, (err, out) => err ? rej(err) : res(out)));
}

function hash(buf) {
  return createHash('sha256').update(buf).digest('hex').slice(0, 8);
}

async function listFiles(dir, exts) {
  const out = [];
  for (const name of await fs.readdir(dir)) {
    const p = path.join(dir, name);
    const st = await fs.stat(p);
    if (st.isFile() && exts.includes(path.extname(name))) out.push(p);
  }
  return out.sort();
}

async function ensureDir(p) {
  await fs.mkdir(p, { recursive: true });
}

async function main() {
  await ensureDir(DIST);
  const terser = await loadTerser();

  // 1. 压缩 JS（40 个，不含 *.test.cjs）
  const jsFiles = (await listFiles(path.join(STATIC, 'js'), ['.js']))
    .filter(f => !f.endsWith('.test.cjs') && !f.endsWith('.test.mjs'));
  // 2. 压缩 CSS（3 个）
  const cssFiles = await listFiles(path.join(STATIC, 'css'), ['.css']);

  const manifest = { js: {}, css: {}, meta: {} };
  let rawJsBytes = 0, minJsBytes = 0, gzJsBytes = 0;
  let rawCssBytes = 0, minCssBytes = 0, gzCssBytes = 0;

  // JS：minify+hash+gzip
  for (const src of jsFiles) {
    const code = await fs.readFile(src, 'utf8');
    rawJsBytes += Buffer.byteLength(code);
    let min;
    try {
      const r = await terser.minify(code, { compress: true, mangle: false, format: { comments: false } });
      if (r.error) throw r.error;
      min = r.code || '';
    } catch (e) {
      // 单文件压缩失败不阻断整体构建，保留原文件并标记
      console.warn(`[build] terser 失败 ${path.basename(src)}: ${e.message} → 保留原内容`);
      min = code;
    }
    const h = hash(Buffer.from(min));
    const base = path.basename(src, '.js');
    const outName = `${base}.${h}.js`;
    const outPath = path.join(DIST, outName);
    await fs.writeFile(outPath, min);
    const gz = await gzipTo(Buffer.from(min));
    await fs.writeFile(outPath + '.gz', gz);
    minJsBytes += Buffer.byteLength(min);
    gzJsBytes += gz.length;
    manifest.js[path.basename(src)] = { hash: h, file: `static/dist/${outName}`, raw: Buffer.byteLength(code), minified: Buffer.byteLength(min), gzip: gz.length };
  }

  // CSS：用 clean-css（库 API，跨平台）；回退：保留原内容
  const CleanCSS = await loadCleanCSS();
  for (const src of cssFiles) {
    const code = await fs.readFile(src, 'utf8');
    rawCssBytes += Buffer.byteLength(code);
    let min = code;
    if (CleanCSS) {
      try {
        const cleaner = new CleanCSS({ returnPromise: false, level: 2 });
        const out = cleaner.minify(code);
        // clean-css 5: out.styles / out.errors
        if (out && out.errors && out.errors.length) {
          console.warn(`[build] clean-css 报错 ${path.basename(src)}: ${out.errors.join('; ')} → 保留原内容`);
        } else if (out && out.styles) {
          min = out.styles;
        }
      } catch (e) {
        console.warn(`[build] clean-css 失败 ${path.basename(src)}: ${e.message} → 保留原内容`);
        min = code;
      }
    }
    const h = hash(Buffer.from(min));
    const base = path.basename(src, '.css');
    const outName = `${base}.${h}.css`;
    const outPath = path.join(DIST, outName);
    await fs.writeFile(outPath, min);
    const gz = await gzipTo(Buffer.from(min));
    await fs.writeFile(outPath + '.gz', gz);
    minCssBytes += Buffer.byteLength(min);
    gzCssBytes += gz.length;
    manifest.css[path.basename(src)] = { hash: h, file: `static/dist/${outName}`, raw: Buffer.byteLength(code), minified: Buffer.byteLength(min), gzip: gz.length };
  }

  manifest.meta = {
    jsFiles: jsFiles.length,
    cssFiles: cssFiles.length,
    rawJsBytes, minJsBytes, gzJsBytes,
    rawCssBytes, minCssBytes, gzCssBytes,
    jsReduction: rawJsBytes ? ((1 - minJsBytes / rawJsBytes) * 100).toFixed(1) : '0',
    jsGzipReduction: rawJsBytes ? ((1 - gzJsBytes / rawJsBytes) * 100).toFixed(1) : '0',
    cssReduction: rawCssBytes ? ((1 - minCssBytes / rawCssBytes) * 100).toFixed(1) : '0',
    cssGzipReduction: rawCssBytes ? ((1 - gzCssBytes / rawCssBytes) * 100).toFixed(1) : '0',
    builtAt: new Date().toISOString()
  };

  await fs.writeFile(path.join(DIST, 'manifest.json'), JSON.stringify(manifest, null, 2));
  console.log(JSON.stringify(manifest.meta, null, 2));
  console.log('[build] OK → web/static/dist/ (manifest.json + *.js/*.css + .gz)');
}

if (CHECK_ONLY) {
  console.log('[check] 仅检查：terser 可加载 + dist 可写');
  await ensureDir(DIST).catch(e => { console.error(e); process.exit(1); });
  await loadTerser().catch(e => { console.error(e); process.exit(1); });
  console.log('[check] OK');
  process.exit(0);
}

main().catch(e => { console.error('[build] FAILED', e); process.exit(1); });
