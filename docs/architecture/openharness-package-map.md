# OpenHarness → CyberStrikeAI 包边界对照与迁移指南

> 来源参考项目：`参考项目/OpenHarness-main`（Python + React Ink TUI，114 测试，whole-product 特征图）。
> 迁移原则：迁移设计思想（非照搬代码），Go 重写，纯新增独立包，零碰并发会话排他文件，向后兼容。
> 文档定位：L1 特征图文档。供 L2（swarm）/L3（cost/permissions/memdir）实施时随时查阅包边界决策。

## 1. Whole-product 特征图匹配

OpenHarness 是一个 whole-product agent harness，覆盖 8 个域。下表对照 CyberStrikeAI 现状与迁移落点：

| 域 | OpenHarness 模块（file:line） | CyberStrikeAI 现状 | 迁移落点 | 状态 |
|----|------------------------------|--------------------|---------|------|
| tools | `src/openharness/tools/*.py`（43+ 工具，base.py/bash_tool/file_*_tool/grep_tool/glob_tool） | `internal/mcp/builtin/` + `tools/*.yaml` + `internal/capability/` | 已有等价（MCP builtin + capability provider），不迁移 | 现状满足 |
| skills | `src/openharness/skills/`（loader.py/registry.py/types.py + bundled/） | `internal/skillpackage/`（content/frontmatter/lock/service/verbs_gate） | 已有等价（skillpackage），不迁移 | 现状满足 |
| memory | `src/openharness/memory/`（manager.py/memdir.py/paths.py/scan.py/search.py/types.py） | `internal/memory/`（被其他会话占用，半成品）+ `~/.claude/projects/.../memory/` | **L3 迁移**：`internal/memdir/`（避开被占用的 internal/memory） | L3 |
| MCP | `src/openharness/mcp/`（client.py/config.py/types.py/__init__.py） | `internal/mcp/`（client_sdk.go/external_manager.go/server.go/ctx_execute_tool.go） | 已有等价（mcp 包，go-sdk），不迁移 client；OpenHarness 的 McpClientManager 是轻量封装，CyberStrikeAI 的 external_manager 已覆盖 | 现状满足 |
| multi-agent (swarm) | `src/openharness/swarm/`（types/registry/in_process/subprocess_backend/worktree/mailbox/team_lifecycle/permission_sync/spawn_utils/lockfile，4899 LOC） | `internal/multiagent/`（coordinator_orchestrator/coordkit/MessageBus）+ `internal/blackboard/` | **L2 迁移**：`internal/swarm/`（双后端 + worktree + mailbox + registry） | L2 |
| permissions | `src/openharness/permissions/`（checker.py/modes.py/__init__.py） | `internal/security/`（rbac.go/scope.go/shellsafe.go/executor.go） | **L3 迁移**：`internal/permissions/`（OpenHarness 的 glob path rule + PermissionMode 三态，与 security/rbac 互补：rbac 管角色，permissions 管 tool-level mode 决策） | L3 |
| sandbox | `src/openharness/sandbox/`（adapter.py） | `internal/ctxsandbox/`（engine.go/store.go）+ `internal/security/executor.go` | 已有等价（ctxsandbox 三级降级 + executor shellsafe），不迁移 | 现状满足 |
| engine (agent loop) | `src/openharness/engine/`（cost_tracker.py/messages.py/query.py/query_engine.py/stream_events.py） | `internal/agent/`（agent.go/token_counter.go/agent_trace.go）+ `internal/multiagent/eino_*` | 部分迁移：cost_tracker → `internal/cost/`（复用 agent.TokenCounter）；agent loop 本体已由 Eino ADK 覆盖 | L3（仅 cost） |

## 2. 迁移决策：为什么是 L2 swarm + L3 cost/permissions/memdir

**选这 3 项的理由**（对照用户原始任务 18/25）：

1. **whole-product 特征图匹配**（用户点 1，"高"）→ L1 本文档。8 域对照已完成，4 域现状满足、3 域迁移（swarm/cost/permissions/memdir）、1 域部分迁移（engine→cost）。
2. **swarm 双后端 + worktree + mailbox + registry**（用户点 2，"高"）→ L2。OpenHarness swarm 是 leader-worker 多 agent 编排的执行底座，CyberStrikeAI 现有 `internal/multiagent/coordinator_orchestrator.go` + `coordkit.MessageBus` 解决了"进程内"协调，但缺"跨进程隔离执行"（subprocess + worktree + 文件 mailbox）。迁移补齐这块。
3. **cost_tracker + mcp client + permissions 独立关注**（用户点 3，"高"）→ L3。OpenHarness 把这三者拆成独立小包，CyberStrikeAI 当前 cost 散落在 agent/multiagent（无独立 CostTracker）、permissions 融在 security/rbac（无独立 tool-level mode 决策器）、mcp client 已有。迁移 cost + permissions 独立化，mcp 保持现状。

## 3. L2 swarm 包边界设计

### 3.1 复用 CyberStrikeAI 已有接缝（不重复造轮子）

| OpenHarness 概念 | CyberStrikeAI 复用点 | 复用方式 |
|-----------------|---------------------|---------|
| `~/.openharness/teams/<team>/agents/<id>/inbox/` | `internal/storage.HomeDir()` → `~/.cyberstrikeai/` | swarm 数据目录挂到统一 home：`<HomeDir>/teams/<team>/agents/<id>/inbox/` |
| `~/.openharness/worktrees/<slug>/` | 同上 | `<HomeDir>/worktrees/<slug>/` |
| 进程内消息传递 | `internal/multiagent/coordkit.MessageBus` | `InProcessBackend` 可选用 MessageBus 做内存态投递（subprocess 后端则用文件 mailbox） |
| 事件流 | `internal/blackboard.Board` | swarm 生命周期事件（spawn/shutdown）可选 Publish 到 board |
| uuid | `github.com/google/uuid`（go.mod 已有） | agent_id/message_id/task_id 生成 |
| logger | `go.uber.org/zap`（go.mod 已有） | 全包统一日志 |

### 3.2 Go 迁移关键改写

| Python 概念 | Go 改写 | 理由 |
|------------|---------|------|
| `async def spawn(...)` / `await` | `func (b *Backend) Spawn(ctx, cfg) (SpawnResult, error)` | Go 用 context + error，无 async/await |
| `Protocol`（runtime_checkable） | `interface` | Go interface 天然鸭子类型 |
| `@dataclass` | `struct` | Go struct + 字段 tag |
| `Literal["subprocess","in_process","tmux","iterm2"]` | `type BackendType string` + 常量 | Go 无 Literal，用 typed string |
| `pathlib.Path` | `string` + `filepath.Join` | Go 用 string 路径 |
| `asyncio.get_event_loop().run_in_executor` | 直接同步 I/O（Go goroutine 天然并发） | Go 无 GIL，文件 I/O 用 Mutex 保护即可 |
| `AsyncExitStack`（mcp client） | `defer Close()` + 显式资源管理 | Go defer 模式 |
| `fnmatch.fnmatch`（glob 匹配） | `path.Match` 或 `filepath.Match` | Go 标准库 |

### 3.3 文件清单（L2 落点，实际交付）

```
internal/swarm/
├── types.go          # BackendType/TeammateIdentity/SpawnConfig/SpawnResult/Message/MailboxMessage + Backend interface
├── mailbox.go        # TeammateMailbox（文件 JSON + 原子写 + lockfile O_EXCL）+ 消息工厂 + validPathComponent 净化
├── worktree.go       # WorktreeManager（git worktree + slug 校验 + worktreeMeta + 清理）
├── in_process.go     # InProcessBackend（goroutine + channel 旁路 + mailbox 持久化）
├── subprocess.go     # SubprocessBackend（os/exec + stdin JSON 行 + 文件 mailbox）
├── registry.go       # DetectBackend + Register/Get/HealthCheck
├── exec_lookpath.go  # exec.LookPath 包装（测试 mock 用）
└── *_test.go         # mailbox_test/worktree_test/backend_test（CGO_ENABLED=0 go test）
```

### 3.4 与 OpenHarness 的差异点（改进/简化）

1. **in_process 后端**：OpenHarness 的 `in_process.py`（693 行）大量处理 prompt 注入/system_prompt 解析。CyberStrikeAI 简化为：in_process 只做"spawn 一个 goroutine 跑用户给的 func + 用 channel/mailbox 通信"，prompt/system_prompt 由上层 `internal/multiagent` 编排层负责（swarm 不重复造 agent loop）。
2. **permission_sync**（1168 行）：OpenHarness 在 swarm agent 间同步权限决策。CyberStrikeAI 简化：swarm 不内置权限同步，权限由 `internal/permissions`（L3）+ `internal/security/rbac` 统一管，swarm 只透传 permissions 列表给后端。
3. **team_lifecycle**（910 行）：OpenHarness 的 team 状态机较重。CyberStrikeAI 简化：team 生命周期由 `internal/multiagent/coordinator_orchestrator` 管，swarm 只提供"spawn/send/shutdown"原语，不维护 team 级状态机。
4. **pane backend（tmux/iterm2）**：OpenHarness 支持可视 pane。CyberStrikeAI 暂不实现 PaneBackend（接口保留，实现返回 ErrNotSupported），因 CyberStrikeAI 是 Web/Electron 形态，不需 tmux pane 可视化。

## 4. L3 cost/permissions/memdir 包边界设计

### 4.1 internal/cost/

| 项 | 说明 |
|----|------|
| 来源 | `engine/cost_tracker.py`（24 行）+ `api/usage.py` UsageSnapshot |
| Go 落点 | `internal/cost/tracker.go` + `internal/cost/pricing.go` + `internal/cost/tracker_test.go` |
| API | `CostTracker{Add(UsageSnapshot) error; Total() UsageSnapshot; Report() Report}` |
| 复用 | `internal/agent.TokenCounter`（tiktoken）算 token；`pricing.go` 内置 model→price 表（input/output per 1K token） |
| 数据结构 | `UsageSnapshot{Model,InputTokens,OutputTokens,CostUSD,Timestamp}` |
| 聚合粒度 | per-session（Add 累加）；Report 按 model 分组 |

### 4.2 internal/permissions/

| 项 | 说明 |
|----|------|
| 来源 | `permissions/checker.py`（106 行）+ `modes.py`（13 行） |
| Go 落点 | `internal/permissions/checker.go`（含 PermissionMode 常量）+ `internal/permissions/glob.go`（fnmatch 等价）+ `internal/permissions/checker_test.go` |
| API | `PermissionMode{Default,Plan,FullAuto}` + `Checker{Evaluate(tool,isReadOnly,path?,command?) PermissionDecision}` |
| 数据结构 | `PermissionDecision{Allowed,RequiresConfirmation,Reason}` + `PathRule{Pattern,Allow}` |
| 与 security 关系 | security/rbac 管角色级（哪些角色能调哪些 tool）；permissions 管 mode 级（default/plan/full_auto 三态 + glob path rule + command deny pattern）。互补不冲突：rbac 通过后再过 permissions checker |

### 4.3 internal/memdir/

| 项 | 说明 |
|----|------|
| 来源 | `memory/memdir.py` + `memory/paths.py` + `memory/scan.py` + `memory/search.py` |
| Go 落点 | `internal/memdir/entries.go`（paths/list/add/remove/prompt/scan 全并入单文件）+ `internal/memdir/entries_test.go` |
| API | `ProjectMemoryDir(homeDir,cwd) (string,error)` + `ListMemoryFiles(homeDir,cwd)` + `AddMemoryEntry(homeDir,cwd,title,content)` + `RemoveMemoryEntry(homeDir,cwd,name)` + `LoadMemoryPrompt(homeDir,cwd,maxLines)` + `ScanMemory(homeDir,cwd,query)`（homeDir 由调用方注入，不直接 import storage） |
| 复用 | `internal/storage.HomeDir()` → `<HomeDir>/memory/<projname>-<sha1[:12]>/` |
| 避让 | `internal/memory/` 被其他会话占用（LightRAG 半成品），故用 `internal/memdir/` 避开 |

## 5. 验收标准

| 节点 | 交付物 | 验收方式 |
|------|--------|---------|
| L1 | 本文档 | 覆盖 8 域 + file:line 证据 + 迁移决策 |
| L2 | `internal/swarm/` 8 文件 | `CGO_ENABLED=0 go test -race ./internal/swarm/` PASS |
| L3 | `internal/cost/` + `internal/permissions/` + `internal/memdir/` | `CGO_ENABLED=0 go test -race ./internal/cost/ ./internal/permissions/ ./internal/memdir/` PASS |
| L-E2E | 包级集成测试 | 独立编译，不依赖被阻塞的 app/security/handler/memory 包 |

## 6. 跨 session 避让清单

`git status` 显示以下文件被其他会话占用，L 批次**不改**这些文件：

- `internal/memory/`（untracked，某会话 LightRAG 半成品，致 `temporal.go:262` 编译失败）
- `internal/knowledge/graph_service.go`（untracked，同上，致 `app.go:384 SetGraphService` 未实现）
- `internal/app/app.go`（M，reactions 接线，K 批次）
- `internal/security/executor*.go`（M，cyberstrikeai-60 拆分）
- `internal/config/config.go`（M，K 批次 reactions）
- `internal/multiagent/eino_*.go` / `runner.go`（M，J 批次）
- `internal/capability/*`（M，J5 批次）
- `web/static/js/*`（M，F 批次前端）
- `go.mod`（不改，L 批次只用已有依赖）

L 批次安全区：`internal/swarm/`（新）、`internal/cost/`（新）、`internal/permissions/`（新）、`internal/memdir/`（新）、`docs/architecture/`（新）。
