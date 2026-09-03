/**
 * K10 PR 风险分级器（对标 agent-orchestrator CI 矩阵）
 *
 * 解析 git diff 文件列表 → 按 RISK_TIERS 分类 → 输出 risk level + step summary。
 *
 * 安全约束：
 *   - 只读 git diff 文件列表，不执行任意代码
 *   - 不调用任意 shell 命令，不 require 任意外部模块（仅用 Node 内置）
 *   - pr-risk.yml 用 pull_request_target + base checkout（不执行 fork 代码）
 *
 * 用法：
 *   node scripts/pr-risk-check.mjs                 # 默认输出 markdown step summary
 *   node scripts/pr-risk-check.mjs --json          # 输出 JSON（含 risk level）
 *   node scripts/pr-risk-check.mjs --github         # 输出 GITHUB_STEP_SUMMARY + 注释
 *   node scripts/pr-risk-check.mjs --base HEAD~1    # 指定 diff base（默认 HEAD~1）
 *
 * 退出码：
 *   0 = 正常（无论 risk level，分级器本身不阻断 PR）
 *   1 = 分级器自身故障（git diff 失败等）
 *
 * 风险分级基于文件路径关键词匹配，详见 docs/FILE-SYSTEM-MAP.md。
 */

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

// ─── RISK_TIERS 定义 ────────────────────────────────────────────────────────
// critical  = 安全/人机回路/能力系统/鉴权——影响授权边界与执行权
// high      = 工作流/多代理/处理器——影响任务编排与运行时路径
// medium    = 成本/监控/输出——影响可观测性与计量，非阻断主路径
// low       = 日志/文档/测试——非生产代码
// test      = 测试文件（_test.go / *.spec.js）——降一级处理
// config    = 配置文件——单独标记，不直接套 tier
const RISK_TIERS = {
  critical: {
    label: '🔴 critical',
    description: '安全/HITL/能力/鉴权——需强制人工审查',
    // 关键词命中即归 critical（安全边界与执行权）
    patterns: [
      /(^|\/)internal\/security\//,
      /(^|\/)internal\/securityevents\//,
      /(^|\/)internal\/hitl\//,
      /(^|\/)internal\/capability\//,
      /(^|\/)internal\/permissions\//,
      /(^|\/)internal\/authctx\//,
      /(^|\/)internal\/ctxsandbox\//,
      /(^|\/)internal\/attackchain\//,
      /(^|\/)internal\/c2\//,
      /(^|\/)internal\/bounty\//,
      /(^|\/)internal\/robot\//,
      /(^|\/)internal\/pluginslot\//,
      /(^|\/)internal\/reactions\//,
      /(^|\/)internal\/dedup\//,
      /(^|\/)internal\/audit\//,
      /(^|\/)internal\/microagent\//,
      /(^|\/)internal\/reasoning\//,
      /(^|\/)internal\/promptassembly\//,
      /(^|\/)internal\/projectprompt\//,
      /(^|\/)internal\/skillpackage\//,
      /(^|\/)internal\/einomcp\//,
      /(^|\/)internal\/knowledge\//,
      /(^|\/)internal\/storage\//,
      /(^|\/)internal\/vision\//,
      /(^|\/)internal\/blackboard\//,
      /(^|\/)internal\/memdir\//,
      /(^|\/)internal\/database\//,
      /(^|\/)internal\/ctxindex\//,
      /(^|\/)internal\/memory\//,
      /(^|\/)internal\/eventstream\//,
      /(^|\/)internal\/tooloutput\//,
    ],
  },
  high: {
    label: '🟠 high',
    description: '工作流/多代理/处理器——影响任务编排与运行时路径',
    patterns: [
      /(^|\/)internal\/workflow\//,
      /(^|\/)internal\/multiagent\//,
      /(^|\/)internal\/orchestrator\//,
      /(^|\/)internal\/swarm\//,
      /(^|\/)internal\/agent\//,
      /(^|\/)internal\/agents\//,
      /(^|\/)internal\/agentfinalizer\//,
      /(^|\/)internal\/handler\//,
      /(^|\/)internal\/app\//,
      /(^|\/)internal\/playbooks\//,
      /(^|\/)internal\/mcp\//,
      /(^|\/)internal\/llm\//,
      /(^|\/)internal\/openai\//,
      /(^|\/)internal\/einoobserve\//,
      /(^|\/)internal\/integration\//,
      /(^|\/)internal\/statusboard\//,
      /(^|\/)internal\/project\//,
      /(^|\/)internal\/metrics\//,
      /(^|\/)internal\/config\//,
      /(^|\/)internal\/cache\//,
      /(^|\/)internal\/sarif\//,
      /(^|\/)internal\/vertical\//,
      /(^|\/)internal\/roi\//,
    ],
  },
  medium: {
    label: '🟡 medium',
    description: '成本/监控/输出——影响可观测性与计量',
    patterns: [
      /(^|\/)internal\/cost\//,
      /(^|\/)internal\/monitor\//,
      /(^|\/)internal\/termout\//,
      /(^|\/)internal\/logger\//,
    ],
  },
  low: {
    label: '🟢 low',
    description: '文档/非生产代码',
    patterns: [
      /(^|\/)docs\//,
      /(^|\/)scripts\//,
      /(^|\/)playbooks\//,
      /(^|\/)knowledge_base\//,
      /(^|\/)roles\//,
      /(^|\/)images\//,
      /(^|\/)\.github\//,
      /(^|\/)README/,
      /(^|\/)LICENSE/,
      /(^|\/)SECURITY\.md$/,
      /(^|\/)AGENTS\.md$/,
      /(^|\/)CLAUDE\.md$/,
      /(^|\/)\.wolf\//,
      /(^|\/)\.claude\//,
      /(^|\/)spec\.md$/,
    ],
  },
  config: {
    label: '⚙️ config',
    description: '配置/构建文件——单独标记，不直接套 tier',
    patterns: [
      /(^|\/)go\.mod$/,
      /(^|\/)go\.sum$/,
      /(^|\/)config\.yaml$/,
      /(^|\/)config\.example\.yaml$/,
      /(^|\/)Makefile$/,
      /(^|\/)Dockerfile$/,
      /(^|\/)\.golangci.*\.yml$/,
      /(^|\/)\.github\/workflows\//,
      /(^|\/)package\.json$/,
      /(^|\/)requirements\.txt$/,
    ],
  },
};

// tier 优先级排序（用于最终 risk level 取最高）
const TIER_PRIORITY = ['critical', 'high', 'medium', 'low', 'config'];

// 测试文件模式——命中则降为 test 分类
const TEST_PATTERNS = [
  /_test\.go$/,
  /\.spec\.[jt]s$/,
  /\/__tests__\//,
  /-test\.[jt]sx?$/,
  /web\/tests\//,
];

function isTestFile(path) {
  return TEST_PATTERNS.some((p) => p.test(path));
}

function classifyFile(path) {
  // 测试文件单独分类（降一级，但保留原 tier 标记）
  if (isTestFile(path)) {
    return { tier: 'test', originalTier: classifyByTiers(path) };
  }
  return { tier: classifyByTiers(path), originalTier: null };
}

function classifyByTiers(path) {
  for (const tier of TIER_PRIORITY) {
    const tierConfig = RISK_TIERS[tier];
    if (tierConfig.patterns.some((p) => p.test(path))) {
      return tier;
    }
  }
  return 'unknown';
}

function getRiskLevel(tiers) {
  // 取所有文件中的最高 tier
  if (tiers.includes('critical')) return 'critical';
  if (tiers.includes('high')) return 'high';
  if (tiers.includes('medium')) return 'medium';
  if (tiers.includes('config')) return 'config';
  if (tiers.includes('low')) return 'low';
  if (tiers.includes('test')) return 'low'; // 纯测试变更归 low
  return 'low'; // unknown 也归 low
}

function runGitDiff(base) {
  // 安全：全部用 execFileSync 数组形式，不经过 shell，防 --base 参数注入。
  const gitDiffOpts = {
    encoding: 'utf8',
    maxBuffer: 10 * 1024 * 1024,
  };
  try {
    const baseRef = base || 'HEAD~1';
    const output = execFileSync('git', ['diff', '--name-only', baseRef], gitDiffOpts);
    return output
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l.length > 0);
  } catch (err) {
    // HEAD~1 可能不存在（单 commit 仓库）——尝试用 staged + unstaged
    try {
      const staged = execFileSync('git', ['diff', '--cached', '--name-only'], gitDiffOpts);
      const unstaged = execFileSync('git', ['diff', '--name-only'], gitDiffOpts);
      const files = (staged + '\n' + unstaged)
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l.length > 0);
      if (files.length === 0) {
        // 回退：git status --porcelain
        const status = execFileSync('git', ['status', '--porcelain'], gitDiffOpts);
        return status
          .split('\n')
          .map((l) => l.slice(3).trim())
          .filter((l) => l.length > 0);
      }
      return files;
    } catch (err2) {
      console.error(`git diff 失败: ${err2.message}`);
      process.exit(1);
    }
  }
}

// 从环境变量指定的文件路径读取 PR 文件列表（GitHub Actions 模式）
// 不依赖 git diff，用于 pull_request_target + base checkout 场景
function readFilesFromEnv() {
  const filePath = process.env.PR_FILES_PATH;
  if (!filePath) {
    return null;
  }
  try {
    const content = readFileSync(filePath, 'utf8');
    return content
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l.length > 0);
  } catch (err) {
    console.error(`读取 PR_FILES_PATH=${filePath} 失败: ${err.message}`);
    process.exit(1);
  }
}

function buildSummary(files, classifications) {
  const tierCounts = {};
  const tierFiles = {};
  for (const tier of [...TIER_PRIORITY, 'test', 'unknown']) {
    tierCounts[tier] = 0;
    tierFiles[tier] = [];
  }

  files.forEach((file, i) => {
    const c = classifications[i];
    if (c.tier === 'test') {
      tierCounts.test++;
      tierFiles.test.push({ file, original: c.originalTier });
    } else {
      tierCounts[c.tier]++;
      tierFiles[c.tier].push(file);
    }
  });

  const riskLevel = getRiskLevel(
    classifications.map((c) => (c.tier === 'test' ? c.originalTier : c.tier))
  );

  let summary = `## 🔍 PR 风险分级报告\n\n`;
  summary += `**总体风险等级**: ${RISK_TIERS[riskLevel]?.label || riskLevel}\n\n`;
  summary += `**文件总数**: ${files.length}\n\n`;

  summary += `### 按风险分布\n\n`;
  summary += `| Tier | 数量 | 说明 |\n`;
  summary += `|------|------|------|\n`;
  for (const tier of TIER_PRIORITY) {
    if (tierCounts[tier] > 0) {
      summary += `| ${RISK_TIERS[tier].label} ${tier} | ${tierCounts[tier]} | ${RISK_TIERS[tier].description} |\n`;
    }
  }
  if (tierCounts.test > 0) {
    summary += `| 🧪 test | ${tierCounts.test} | 测试文件（降一级处理） |\n`;
  }
  if (tierCounts.unknown > 0) {
    summary += `| ❓ unknown | ${tierCounts.unknown} | 未匹配任何 tier |\n`;
  }
  summary += `\n`;

  // 列出 critical / high 文件清单
  for (const tier of ['critical', 'high']) {
    if (tier === 'critical' ? tierCounts.critical > 0 : tierCounts.high > 0) {
      const list = tier === 'critical' ? tierFiles.critical : tierFiles.high;
      summary += `### ${RISK_TIERS[tier].label} ${tier} 文件清单（需强制审查）\n\n`;
      for (const file of list) {
        summary += `- \`${file}\`\n`;
      }
      summary += `\n`;
    }
  }

  summary += `---\n_K10 PR 风险分级器生成（只读 git diff，不执行任意代码）_\n`;
  return { summary, riskLevel, tierCounts, tierFiles };
}

function main() {
  const args = process.argv.slice(2);
  const jsonMode = args.includes('--json');
  const githubMode = args.includes('--github');
  const filesFromEnv = args.includes('--files-from-env');
  const baseIdx = args.indexOf('--base');
  const base = baseIdx >= 0 ? args[baseIdx + 1] : null;

  // --files-from-env 优先（GitHub Actions 模式，不依赖 git diff）
  let files;
  if (filesFromEnv) {
    files = readFilesFromEnv();
    if (files === null) {
      console.error('--files-from-env 需要 PR_FILES_PATH 环境变量');
      process.exit(1);
    }
  } else {
    files = runGitDiff(base);
  }

  if (files.length === 0) {
    if (jsonMode) {
      console.log(JSON.stringify({
        risk_level: 'low',
        files: [],
        tier_counts: {},
        message: '无文件变更',
      }, null, 2));
      return;
    }
    const summary = '## 🔍 PR 风险分级报告\n\n**总体风险等级**: 🟢 low\n\n无文件变更。\n';
    if (githubMode && process.env.GITHUB_STEP_SUMMARY) {
      writeFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
    } else {
      console.log(summary);
    }
    return;
  }

  const classifications = files.map((f) => classifyFile(f));
  const { summary, riskLevel, tierCounts, tierFiles } = buildSummary(files, classifications);

  if (jsonMode) {
    console.log(JSON.stringify({
      risk_level: riskLevel,
      risk_label: RISK_TIERS[riskLevel]?.label || riskLevel,
      file_count: files.length,
      tier_counts: tierCounts,
      files: files.map((file, i) => ({
        file,
        tier: classifications[i].tier,
        original_tier: classifications[i].originalTier,
      })),
      critical_files: tierFiles.critical,
      high_files: tierFiles.high,
    }, null, 2));
    return;
  }

  if (githubMode && process.env.GITHUB_STEP_SUMMARY) {
    writeFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
  }

  // 默认 markdown 输出到 stdout（githubMode 也打印供 workflow 使用）
  console.log(summary);
}

main().catch((err) => {
  console.error(`pr-risk-check 失败: ${err.message}`);
  process.exit(1);
});
