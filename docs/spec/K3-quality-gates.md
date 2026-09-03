# Spec: K3 — 质量门（quality gates）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

建立工程化质量门：`-race` 测试、覆盖率检查（diff-cover 80% 门禁）、安全扫描（gitleaks + govulncheck）、CI 矩阵升级。对标 agent-orchestrator CI 矩阵，把"测试通过"从手动判断提升为 CI 强制门禁。

## Tech Stack

- Go 1.25 测试工具链：`go test -race`、`go test -coverprofile`、`go tool cover`
- GitHub Actions：`ci.yml`（test-race）、`quality.yml`（coverage + security job）
- 安全扫描：`gitleaks-action@v2`（密钥扫描）、`govulncheck`（依赖漏洞扫描）
- Makefile：`test-race` / `cover` 目标

## Commands

```bash
make test-race              # 带 -race（需 CGO）
make cover                  # 覆盖率到 cover.out + HTML 报告
go test ./... -count=1      # 普通测试
# CI 触发：
#   ci.yml      → go test -race -count=1 ./...
#   quality.yml → coverage job（cover.out artifact）+ security job（gitleaks + govulncheck）
```

## Project Structure

```
Makefile                              → test-race（CGO_ENABLED=1 go test -race）+ cover（coverprofile）
.github/workflows/ci.yml              → K3：Test 改 go test -race -count=1 ./...
.github/workflows/quality.yml          → K3：coverage job（cover.out artifact）+ security job（gitleaks + govulncheck）
docs/                                 → 覆盖率报告落盘（cover.out + HTML）
```

## Code Style

- Makefile 目标命名：`test-race` / `cover`（kebab-case，匹配现有目标风格）
- CI workflow 注释标注批次来源（`# K3：` 前缀，便于回溯）
- 覆盖率门禁：diff-cover 80%（PR 差异覆盖率，非全量覆盖率）

## Testing Strategy

- `test-race`：`CGO_ENABLED=1 go test -race -count=1 ./...`（race 检测，需 CGO）
- `cover`：`go test -coverprofile=cover.out -count=1 ./internal/...` + `go tool cover -func`
- CI 矩阵：ci.yml 跑 test-race；quality.yml 跑 coverage + security
- 回归底线：双路径（CGO=1 mingw / pure-go tag）不新增 FAIL；已知基线 FAIL 集合记录在 workflow_status.md

## Boundaries

- **Always**：`-race` 需 CGO；覆盖率门禁用 diff-cover（PR 差异，非全量）；CI 注释标批次来源
- **Ask first**：改 CI 触发条件（push/PR/target-branch）；改覆盖率门禁阈值（80%）；引入新安全扫描工具
- **Never**：为让测试变绿跳过 `-race`；改全量覆盖率为 diff-cover 后删除全量报告；CI 失败时自动重试掩盖 flakes

## Success Criteria

1. Makefile `test-race` 目标存在（CGO_ENABLED=1 go test -race）✅ done
2. Makefile `cover` 目标存在（coverprofile + cover -func）✅ done
3. ci.yml Test 改 `go test -race -count=1 ./...` ✅ done
4. quality.yml 新增 coverage job（cover.out artifact）✅ done
5. quality.yml 新增 security job（gitleaks-action@v2 + govulncheck）✅ done
6. 双路径测试不新增 FAIL（基线 FAIL 集合记录在 workflow_status.md）✅ done

## Open Questions

- diff-cover 80% 门禁是否在 CI 强制阻断（当前只生成 cover.out artifact，未阻断 PR）—— 待后续批次评估
