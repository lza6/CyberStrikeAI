# Spec: K10 — 工程化 CI 矩阵升级（engineering CI matrix）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

对标 agent-orchestrator CI 矩阵，升级工程化门禁：golangci-lint v2（zero-findings blocking）+ gofmt gate + go-version-file（防 CI 与 module 漂移）+ PR 风险分级器（按文件路径 Tier 分级，只读 git diff，不执行任意代码）。

## Tech Stack

- golangci-lint v2.0.2（v2 schema，zero-findings 严格策略）
- GitHub Actions：`ci.yml`（gofmt gate + golangci-v2 job）、`quality.yml`（go-version-file）、`pr-risk.yml`（PR 风险分级）
- Node.js 内置模块（`child_process`、`fs`、`path`）—— PR 风险分级器只用 Node 内置，不 require 外部模块
- `pull_request_target` + base checkout（PR 风险分级器不执行 fork 代码）

## Commands

```bash
# golangci-lint v2（本地）
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.0.2
golangci-lint run -c .golangci.v2.yml ./...

# PR 风险分级器（本地）
node scripts/pr-risk-check.mjs                 # 默认输出 markdown step summary
node scripts/pr-risk-check.mjs --json           # 输出 JSON（含 risk level）
node scripts/pr-risk-check.mjs --github         # 输出 GITHUB_STEP_SUMMARY + 注释
node scripts/pr-risk-check.mjs --base HEAD~1    # 指定 diff base
# CI 触发：
#   ci.yml      → gofmt gate + go vet + go build + go test -race + golangci-v2 job
#   quality.yml → go-version-file + coverage + security
#   pr-risk.yml → PR 风险分级器（pull_request_target + base checkout）
```

## Project Structure

```
.golangci.v2.yml                     → K10：golangci-lint v2 配置（zero-findings，govet/errcheck/staticcheck/gosec/ineffassign/gofmt）
.golangci.yml                        → v1 向后兼容（与 v2 并存，CI 切 v2）
.github/workflows/ci.yml             → K10：gofmt gate + go-version-file + golangci-v2 job
.github/workflows/quality.yml         → K10：go-version-file（4 处，防 CI 与 module 漂移）
.github/workflows/pr-risk.yml         → K10：PR 风险分级器（pull_request_target + base checkout）
scripts/pr-risk-check.mjs            → K10：PR 风险分级器脚本（只读 git diff，不执行任意代码）
docs/FILE-SYSTEM-MAP.md              → K10：PR 风险分级器 Tier 映射（critical/high/medium/low/test/config）
```

## Code Style

- golangci-lint v2 配置：`default: none` + `enable:` 显式列表（zero-findings 严格策略）
- PR 风险分级器：Node ESM（`import { execSync } from 'node:child_process'`），只用内置模块，无外部依赖
- CI workflow 注释标批次来源（`# K10：` 前缀）

## Testing Strategy

- golangci-lint v2：`golangci-lint run -c .golangci.v2.yml ./...` zero-findings（无违规才通过）
- gofmt gate：`gofmt -l .` 未格式化文件非空则 exit 1
- PR 风险分级器：`node scripts/pr-risk-check.mjs --json` 输出 risk level；退出码 0=正常，1=分级器自身故障（分级器不阻断 PR）
- 风险分级基于文件路径关键词匹配（见 `docs/FILE-SYSTEM-MAP.md`）：critical（安全/HITL/能力/鉴权）/ high（工作流/多代理/处理器）/ medium（成本/监控/输出）/ low（日志/文档/测试）/ test（_test.go 降一级）/ config

## Boundaries

- **Always**：golangci-lint v2 zero-findings blocking；gofmt gate exit 1；go-version-file（防漂移）；PR 风险分级器只读 git diff 不执行任意代码；pr-risk.yml 用 pull_request_target + base checkout
- **Ask first**：改 golangci-lint 版本（v2.0.2）；改 zero-findings 为 warning-only；改 PR 风险分级器 Tier 关键词；改 go-version-file 为硬编
- **Never**：改 PR 风险分级器执行 fork 代码（必须 pull_request_target + base checkout）；改 zero-findings 为允许违规；删除 gofmt gate

## Success Criteria

1. `.golangci.v2.yml` v2 schema 配置齐全（govet/errcheck/staticcheck/gosec/ineffassign/gofmt）✅ done
2. ci.yml gofmt gate（未格式化文件非空则 exit 1）✅ done
3. ci.yml go-version-file（替代硬编 '1.25'）✅ done
4. ci.yml golangci-v2 job（zero-findings blocking）✅ done
5. quality.yml go-version-file（4 处）✅ done
6. pr-risk.yml PR 风险分级器（pull_request_target + base checkout）✅ done
7. scripts/pr-risk-check.mjs 只用 Node 内置模块（无外部依赖）✅ done
8. docs/FILE-SYSTEM-MAP.md Tier 映射齐全 ✅ done

## Open Questions

- golangci-lint v2 是否在本地强制运行（当前 CI 运行，本地可选）—— 后续批次评估 pre-commit hook
