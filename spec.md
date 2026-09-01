# Spec: CyberStrikeAI I 批次 —— 参考项目设计迁移落地（生产终审后续）

> 本 Spec 遵循 spec-driven-development 工作流。I 批次 = 用户明确授权的参考项目设计思想迁移 + 遗留 P1/P2 落地。

## Objective

把 11 个参考项目（Pentest-Swarm-AI/strix/caveman/VulnClaw/bettercap 等）中**已验证的设计思想**迁移进主项目（Go 重写，不搬 Python），补齐 5.3-5.7 节工程/性能/安全差距。用户已在生产环境部署，一切改动以**向后兼容、可回滚、不破坏现有 106 工具/74 skill/16 agent 加载**为前提。

**用户裁决的架构分歧（覆盖此前的"不建议"判断）**：
1. Redis 允许引入（可选依赖，`cache.enabled: false` 默认关，启用后 Cache-Aside 降 DB 负载）。
2. Python 运行时逻辑允许调用（桌面壳内嵌 Python 3.13.5 已存在；服务端通过工具 yaml 的 command 执行 Python 脚本——不引入服务端 Python 依赖，只作为工具进程）。
3. WiFi/BLE 等无线攻击模块**允许集成**（作为工具 yaml + MCP 工具，不是 Go 代码——本机有 aircrack-ng 等二进制即可用）。
4. Electron 原生外壳要增强（原生菜单已有，补托盘/原生对话框/启动画面），Web UI 仍为界面主体。

## Tech Stack

- Go 1.25 + mattn/go-sqlite3 CGO（不变）
- Redis：go-redis v9（**可选依赖**，lazy 初始化，未配置时零开销）
- Electron 31.7.7（不变，增强外壳）
- 前端原生 JS（模块化拆分 chat.js/settings.js，ES module + 兼容回退）

## Commands

```
Build:  go build -o cyberstrike-ai.exe cmd/server/main.go   （需 CGO_ENABLED=1 CC=gcc + mingw64 PATH）
Vet:    go vet ./...
Test:   go test ./internal/security/ ./internal/skillpackage/ -count=1
Full:   go test ./... -count=1
Desktop:cd desktop && npx electron-builder --win nsis
```

## Project Structure（新增部分）

```
internal/security/shellsafe.go       → shellsafe.Parse（quote-aware 元字符拒绝，Pentest-Swarm-AI 移植）
internal/security/shellsafe_test.go  → 22+ 注入 case
internal/security/highimpact.go      → HIGH_IMPACT 工具审批集（mcpstrike 思想）
internal/security/highimpact_test.go
internal/security/scope.go           → CIDR/Domain/Port/Excluded scope validator
internal/security/scope_test.go
internal/skillpackage/lock.go        → skills-lock.json 生成+校验（caveman 思想）
internal/skillpackage/lock_test.go
internal/skillpackage/verbs_gate.go  → skill→tool 引用漂移门
internal/skillpackage/verbs_gate_test.go
skills-lock.json                     → 74 skill 锁清单（生成产物）
internal/multiagent/eino_turn_limiter.go  → TurnToolCallLimiter（strix 思想）
internal/multiagent/eino_turn_limiter_test.go
internal/cache/cache.go              → Cache-Aside 抽象（memory 默认实现 + Redis 可选）
internal/cache/cache_test.go
tools/wireless/*.yaml                → aircrack-ng/bettercap/kismet/hcxdump 等 8 个无线工具
desktop/src/tray.js                  → Electron 托盘
desktop/src/splash.html              → 启动画面
web/static/js/chat/                  → chat.js 模块化拆分产物（chat-core/input/render/tools）
docs/adr/                            → ADR-0001~0006（架构决策记录）
docs/SOP.md                          → 开发/发布/回滚 SOP
```

## Code Style

```go
// 匹配现有包风格：包注释 + 契约注释 + fail-closed
// Package shellsafe provides quote-aware command parsing that rejects
// unquoted shell metacharacters. Defence-in-depth: scope and allowlist
// checks run separately.
func Parse(cmd string) ([]string, error) { ... }
```

## Testing Strategy

- 新增纯函数 100% 单测（table-driven，匹配 internal/security 现有测试风格）
- 每个中间件带 `Test<Feature>` 回归
- E2E：curl 实测（限流 429 / 安全头 / 高危工具审批 403）
- 回归底线：不新增任何 FAIL（基线 FAIL 集合已记录于 workflow_status.md）

## Boundaries

- **Always**: 每个 P 项先写测试再实现；go vet/build 过了才算完；不动 config.yaml（运行态），config.example.yaml 同步
- **Ask first**: 改 executor.go 现有函数签名；改 RBAC 权限目录；schema 迁移
- **Never**: 删除现有工具/skill/agent；改 local_mode 语义；硬编码 API key/上游地址；把 mock 当实现

## Success Criteria

1. `shellsafe.Parse` 拦截 22+ 注入 case，executor 两条 exec 路径（824/827 行）全部过 shellsafe
2. HIGH_IMPACT 集（≥8 个破坏性工具）默认要求审批，未审批 403 + 审计记录
3. scope validator 支持工具 yaml `scope:` 字段，越界目标 400
4. `skills-lock.json` 覆盖全部 skill（SHA256），锁校验失败启动 Warn；verbs-gate 扫描 skill 引用→不存在工具报 Violation 清单
5. TurnToolCallLimiter：单 turn 工具调用 >N 拦截（N 可配，默认 25）；`tool_call_ids` 全 turn 唯一
6. cache.Cache-Aside：memory 实现 + Redis 可选（`cache.driver: memory|redis`），未配置 Redis 时 memory 兜底且零告警
7. 无线工具 8 个 yaml 加载进 MCP 池（/api/config/tools 可见）
8. Electron 外壳：托盘（显示/隐藏/退出）+ 启动画面（后端 ONLINE 后淡出）+ 原生错误对话框
9. chat.js 拆分后 node --check 全过、行为不变（DOM 结构/事件绑定/i18n 不动）
10. ADR 6 篇 + SOP 文档落盘；全链路 E2E 复验（启动→免登录→对话→工具→审批拦截）

## Open Questions

- 无（用户已裁决全部 10.1-10.5 假设：授权实施、允许 commit/push、local_mode 断言已落地、glm-5.2 本地代理可用于 LLM 验证、批次4 串行）
