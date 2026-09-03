# CLAUDE.md — CyberStrikeAI 项目上下文

> AI 原生安全执行中枢。Go 1.25 + Eino 智能体 + MCP 原生工具 + RAG + 攻击链建模。面向**已授权**的安全任务。
> 安装、特性总览、界面预览见 [README_CN.md](README_CN.md) / [README_EN.md](README_EN.md)，此处只放项目内规则与可验证事实。

---

## Rules（最高优先级，覆盖一切）

1. **授权边界**：WebShell、C2、无线攻击、漏洞利用等高风险能力仅限自有系统或已获明确书面授权的目标。改动安全闸门前先读 [安全模型](docs/zh-CN/security-model.md) 与 [安全加固](docs/zh-CN/security-hardening.md)。
2. **fail-closed**：所有安全闸门（`shellsafe` / `scope` / `highimpact` / `capability` / `turnlimiter`）默认拒绝，不得为让测试变绿而放宽断言或改 fail-open。
3. **向后兼容、可回滚**：用户已在生产部署。改动不得破坏现有 108 工具 / 74 skill / 16 agent 加载，不得改 `config.yaml`（运行态）语义；新增配置走 `config.example.yaml` 同步。
4. **真实闭环**：禁止把"有代码/有按钮"包装成"已完成"。声称 done 必须附 `go vet/build/test` 真实输出或 curl/Playwright 真实链路证据。`workflow_status.md` 只记事实与证据，只有观察到交付物才标 done。
5. **付费 API 红线**：真实付费 LLM/AI 调用预算默认为 0。用 Mock/fixture 验证参数拼装、轮询、回调、超时、重试、限流、幂等。真实语义评测属付费红线，不得伪造。
6. **Windows 规则**：禁用 `.sh` 脚本写业务逻辑（`run.sh` 仅为部署引导，已存在）；命令链接用 `; if($?) {}`；查找可执行用 `where.exe`；搜索用内置 ripgrep。

## Tech Stack

- **后端**：Go 1.25.0（以 `go.mod` 为准）。SQLite 双驱动：`mattn/go-sqlite3`（CGO）+ `modernc.org/sqlite`（pure-go，`-tags sqlite_pure_go`）。Eino 智能体编排。MCP SDK `modelcontextprotocol/go-sdk v1.3.1`。
- **前端**：原生 JS + ES module，`web/build.mjs`（terser + clean-css + 静态 gzip），Node ≥20（见 `web/package.json` engines）。**不引入** React/Vue 等框架。
- **桌面壳**：Electron 31.7.7 + electron-builder 25.1.8（见 `desktop/package.json`），Web UI 仍为界面主体，原生外壳补托盘/启动画面/原生对话框。
- **Lint**：`.golangci.yml` 启用 govet/errcheck/staticcheck/gosec/ineffassign。
- **MCP**：`.mcp.json` 配置 graft（代码图谱，本会话可能连接超时，失败时回退 Grep/Read）。

## Commands

```bash
# 启动（默认 --https，本机自签证书；--http 切明文）
./run.sh                    # 一键：检查环境 → venv → go mod download → build → 启动
go run cmd/server/main.go --https

# 构建（CGO 路径需 mingw64 gcc 在 PATH，或显式 CC）
CGO_ENABLED=1 CC=/c/mingw64/bin/gcc.exe go build -o cyberstrike-ai.exe cmd/server/main.go
make build

# 静态检查
go vet ./...
make vet

# 测试 —— 双路径互验（CGO=1 mingw / pure-go tag）
CGO_ENABLED=1 CC=/c/mingw64/bin/gcc.exe go test ./... -count=1
CGO_ENABLED=0 go test -tags sqlite_pure_go ./... -count=1
make test-race              # 带 -race（需 CGO）
make cover                  # 覆盖率到 cover.out

# 安全/供应链门禁
make skills-lock            # 生成 skills-lock.json（SHA256 锁）
make verbs-gate             # 扫 skill→tool 引用漂移（report 模式 exit 0）
make verbs-gate-strict      # 严格模式，发现幽灵工具 exit 1

# 后端热点行为验证
make perf-cache             # /api/config cache-aside 命中 vs 失效
make perf-db-smoke          # SQLite 并发写无 "database is locked"

# 前端
cd web && npm run build     # 压缩 + 文件哈希 + 静态 gzip
cd web && npm run check     # 只校验不产出

# 桌面打包
cd desktop && npx electron-builder --win nsis
```

测试基线：`go test ./...` 双路径不新增 FAIL。已知基线 FAIL 集合记录在 `workflow_status.md`。

## Conventions

- **包组织**：`internal/<domain>/`，高内聚低耦合，典型 200-400 行/文件，800 上限。`cmd/` 只放入口（`cmd/server`、`cmd/mcp-stdio`、`cmd/genlock`、`cmd/verbs-gate` 等）。
- **错误处理**：`fmt.Errorf("failed to X: %w", err)` 带上下文包装；安全路径不得静默吞错。
- **不可变优先**：返回新对象而非就地修改。
- **安全闸门分层**（defense-in-depth，各层独立运行）：`internal/security/shellsafe.go`（quote-aware 元字符拒绝，executor 两条 exec 路径必过）→ `scope.go`（CIDR/Domain/Port/Excluded）→ `highimpact.go`（≥18 破坏性工具审批集，未审批 403 + 审计）→ `internal/capability`（plan/validate/rollback 生命周期）→ `internal/multiagent/eino_turn_limiter.go`（单 turn 工具调用上限，防退化卡死）。
- **工具/skill/agent 纯声明式**：工具是 `tools/*.yaml`（108 个），skill 是 `skills/*/SKILL.md`（74 个，有 `skills-lock.json` 锁），agent 是 `agents/*.md`，role 是 `roles/*.yaml`（12+）。新增不改 Go 代码。
- **配置**：`config.example.yaml` 是权威模板，`config.yaml` 是运行态（不提交真实凭证）。AI 通道在 `ai.channels`，`ai.default_channel` 为新对话默认。
- **数据目录**：`~/.cyberstrikeai/`（home 迁移已落地，K4 默认接入）。
- **E2E**：`web/tests/e2e/` 用 Playwright；首屏默认 dashboard 非 chat；`authPermissions.size>0` 为登录锚点；headless Chromium 偶发卡死用 `retries=1` 兜底。

### 已知测试坑（避免重复踩）

1. **stub 驱动**：测试不得绕过 `sqliteDriverName()/sqliteDSN()` 适配层直接 `sql.Open("sqlite3")`。
2. **TempDir 文件锁**：handler 测试用 `t.TempDir()` 时必须 `defer db.Close()`，否则 Windows unlinkat 撞锁。
3. **时间戳字符串比较**：同秒内 RFC3339 纳秒精度字符串比较失效，测试需显式偏移时间（+2s 同格式写回）。

## Boundaries

- **Always**：改安全闸门先写测试再实现；`go vet`/`build` 过了才算完；`config.example.yaml` 与代码同步。
- **Ask first**：改 `executor.go` 现有函数签名；改 RBAC 权限目录；改 SQLite schema 迁移；升级核心框架（Eino/MCP SDK/Electron）。
- **Never**：删除现有工具/skill/agent；改 `local_mode` 语义；硬编码 API key/上游地址；把 Mock 当生产实现；为通过测试削弱断言；未授权 `git reset --hard` / force push / amend 用户历史。

## 关键文档入口

- 规划与验收：`spec.md`（I 批次设计迁移）、`workflow_status.md`（单一事实源，节点验收证据）、`docs/spec/`（K0a/K0b/K0c/K3/K4/K8/K9/K10 八批次回溯 spec）。
- 架构：`docs/zh-CN/architecture.md`、`docs/zh-CN/security-model.md`、`docs/zh-CN/tool-execution-governance.md`、`docs/zh-CN/multi-agent-eino.md`。
- 运维：`docs/zh-CN/configuration.md`、`docs/zh-CN/configuration-profiles.md`、`docs/zh-CN/security-hardening.md`、`docs/zh-CN/runbooks.md`。
- 集成：`docs/zh-CN/api-reference.md`、`docs/zh-CN/mcp-federation.md`、`docs/zh-CN/plugin-development.md`。
- 安全披露：`SECURITY.md`。

## Spec-Driven Development（先规范后编码，CRITICAL）

基于 [github/spec-kit](https://github.com/github/spec-kit) v1.0.4（Spec-Driven Development 工具包，`specify-cli` + `/speckit-*` slash 命令）+ [addyosmani/agent-skills](https://github.com/addyosmani/agent-skills) 的 `spec-driven-development` skill（SKILL.md 规范格式）。本仓库不引入 `specify-cli` 工具依赖，直接用 `docs/spec/` 目录承载 spec 文档。

**下次改代码前必须先读 `docs/spec/`**（强制流程，判断 spec 是否过时）：

1. **定位 spec**：按改动文件路径找对应批次 spec（改 `internal/vertical/` → 读 `docs/spec/K0a-vertical.md`；改 `internal/blackboard/` → 读 `K0b-blackboard-sqlite.md`；改 `internal/skillpackage/` → 读 `K0c-skillpackage-recursive.md`；改 Makefile/CI → 读 `K3-quality-gates.md` / `K10-engineering-ci.md`；改 `internal/config/` home 逻辑 → 读 `K4-unified-home.md`；改 `internal/sarif/` 或 `internal/attackchain/` → 读 `K8-sarif-attackchain.md`；改 `internal/multiagent/` StuckDetector 或 `internal/reactions/` lifecycle → 读 `K9-orchestration-strategy.md`）。
2. **对照 Success Criteria**：grep 源码 + 跑测试，检查 spec 标注的 "done" 项是否仍成立。
3. **判断是否过时**：若源码与 spec 的 Project Structure / Code Style / Boundaries 不一致 → spec 过时，**先更新 spec 再改代码**；若一致 → 按 spec 的 Boundaries（Always / Ask first / Never）约束改动。
4. **改代码后同步 spec**：若改动影响 Project Structure / Success Criteria / Open Questions → 同步更新 spec 文件 + 在 `workflow_status.md` 记证据。
5. **新批次**：按 `docs/spec/README.md` § 5 的工作流（Specify → Plan → Implement → Verify → Review → Converge）走，每节点有交付物 + 验证 + 完成标准。

**spec 文档 8 段固定结构**（匹配 agent-skills SKILL.md 规范）：Objective / Tech Stack / Commands / Project Structure / Code Style / Testing Strategy / Boundaries（Always/Ask first/Never）/ Success Criteria + Open Questions。详见 `docs/spec/README.md`。
