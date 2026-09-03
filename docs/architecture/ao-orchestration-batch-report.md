# AO 批次 · agent-orchestrator 三项编排能力迁移 — 变更报告（2026-09-02）

> 报告人：cyberstrikeai-b5 会话。台账见 `workflow_status.md` AO 批次段落。
> 状态：**实施 + E2E + 独立审计（CONDITIONAL PASS）+ 审计修复复验 全部完成**。

## 零、独立审计结论（AO-Audit）

**总体：CONDITIONAL PASS** — 无 CRITICAL、无伪实现、无伪绿（33 测试 0 SKIP 0 FAIL、断言非恒真）。
DeriveStatus 与参考项目逐函数对照语义等价；SQL 全参数化；路径遍历/命令注入实测注入向量全部被拒；`-race` 四包 PASS。

**审计发现问题 × 修复对照（全部修复并复验）**：

| # | 严重度 | 问题 | 修复 | 复验 |
|---|--------|------|------|------|
| 1 | MEDIUM | managedRoot 为空时 Destroy/ForceDestroy 跳过校验，可删任意路径 | workspace_git.go：managedRoot 空时用 fallback 根（tmp/workspace/workers）仍强制 validateManagedPath | TestGitWorkspace_DestroyWithEmptyManagedRoot PASS（逃逸路径被拒） |
| 2 | MEDIUM | SearchEvents 用 context.Background() 无法取消，goroutine 可能阻塞泄漏连接 | 新增 SearchEventsCtx（带取消变体），SearchEvents 委托之；doc 注释披露接口约束 | vet+test PASS |
| 3 | MEDIUM | E2E 未串联 cdc.Poller/Broadcaster（live push 段只被单测覆盖） | e2e_test.go 新增阶段 4：StoreSource(SQLiteStore)→Poller→Broadcaster→看板订阅者，断言 ≥3 事件升序到达 + cursor 推进 | E2E PASS（CGO） |
| 4 | MEDIUM | E2E 无 cgo build tag，纯 Go 矩阵必红（mattn stub ping 失败） | e2e_test.go 加 //go:build cgo | CGO_ENABLED=0 全矩阵 ok |
| 5 | LOW | branch 已存在路径 --force 允许同 branch 双 worktree（互污染）；ErrBranchCheckedOutElsewhere 死哨兵 | addWorktree 复用前 findWorktreeByBranch 检查占用，被占返回 ErrBranchCheckedOutElsewhere（哨兵接线） | TestGitWorkspace_BranchCheckedOutElsewhere PASS |
| 6 | LOW | sanitize 静默改写与参考项目"报错拒绝"策略不一致 | 保留主项目一致策略，doc 注释披露差异与碰撞面 | 静态披露 |
| 7 | LOW | StatusExited→NeedsYou 映射语义待产品确认 | 注释披露语义依据与调整方向 | 静态披露 |
| 8 | LOW | 三处死代码（errClosedRowSentinel/skipIfNoCGO/ErrBranchCheckedOutElsewhere） | 前两者删除；ErrBranchCheckedOutElsewhere 由 #5 接线 | grep=0 + 编译过 |
| 9 | LOW | runGit 异常块注释格式 | 改常规行注释 | 静态 |

**修复后回归（全绿）**：
- `go vet` 5 包 EXIT=0
- CGO 矩阵 `go test -count=1` 5 包全 ok（含 2 个新增审计修复验证测试）
- 纯 Go 矩阵 `CGO_ENABLED=0 go test -count=1` 5 包全 ok
- `-race`（mingw gcc）：statusboard/cdc/pluginslot/orchestrator 全 ok + eventstream SQLiteStore 测试 ok（eventstream 包其余测试的 race 报警属 OpenHands 批次既有测试自身变量竞态，非 AO 代码，已披露不阻塞）

## 零-B、审计披露的跨会话风险

审计期间观察到并发会话于 23:51 修改 AO 声明安全区内的 `internal/pluginslot/notifier.go`（瞬时编译失败后自恢复）。建议各会话重新确认文件避让边界（AO 安全区：pluginslot/workspace*.go、statusboard/、orchestrator/、eventstream/sqlite_store*.go）。

## 一、任务契约

**来源**：参考项目 `参考项目/agent-orchestrator/backend`（Untrivial，Go 1.25 + sqlc + modernc/sqlite + git CLI）。

**三项迁移能力**：
1. **编排守护 daemon**（event watcher → state → agent actions）
2. **CDC + status-derivation 活看板**（从持久变更流派生攻击状态板）
3. **worker 隔离**（own worktree/branch，每个 pentest task = 隔离 context + own artifact scope）

**用户决策**（已确认）：
- 接线方式：混合——daemon/看板纯新增包暂不接 app.go；worker 扩展 pluginslot.SlotWorkspace
- worker 隔离强度：两者都支持（配置切换 directory | git-worktree）
- CDC 载体：复用 eventstream + 新增 SQLiteStore

**授权边界**：本地修改 + 非破坏性验证；不推送不部署；零碰并发会话禁区（app.go/config.go/go.mod/security/multiagent/capability）。

## 二、实际修改（全部为新增文件，零修改现有行）

### 机制 2：CDC + status 活看板

| 文件 | 行数 | 内容 |
|------|------|------|
| `internal/statusboard/status.go` | 367 | DeriveStatus 派生纯函数（优先级链 terminated>active>exited>waiting/blocked>PR worst-severity>no_signal>idle）+ BuildStacks（父 PR 阻塞子 PR）+ ColumnFor（SessionStatus→看板五列映射）+ ActivityState/SessionStatus/SessionFacts/PRFacts/CIState/ReviewDecision/Mergeability 全枚举 |
| `internal/statusboard/status_test.go` | 200 | 表驱动 4 组测试：优先级 8 case / no_signal 4 case / PR 管线+worst-wins 14 case / stack 3 段 / 列映射 14 case |
| `internal/eventstream/sqlite_store.go` | ~430 | SQLiteStore（注入 `*sql.DB`，leaf 包零 sqlite 驱动依赖）：change_log 表（seq AUTOINCREMENT + json_valid CHECK + 3 索引）+ Append/GetEvent/LatestEventID/EventsAfter/SearchEvents + envelope 编解码 + 未知事件类型 genericEvent 还原 |
| `internal/eventstream/sqlite_store_test.go` | ~330 | //go:build cgo，真 SQLite 7 case：roundtrip 字段还原 / LatestEventID COALESCE / EventsAfter 升序+limit / SearchEvents IncludeTypes / E2E AddEvent→持久化→fan-out→cause 链 / nil-safe / 重启恢复 curID |
| `internal/statusboard/cdc/cdc.go` | ~300 | Poller（100ms ticker + 512 batch + SeekToHead + 幂等守卫）+ Broadcaster（panic recover 隔离 + unsubscribe）+ StoreSource（eventstream.Store→CDC Source 适配层，after+1 语义） |
| `internal/statusboard/cdc/cdc_test.go` | ~360 | 6 case：Poll 排空+推进 cursor / SeekToHead 跳历史 / Start live 投递+顺序 / panic 隔离 / unsubscribe / StoreSource 适配 |

### 机制 3：worker 隔离

| 文件 | 行数 | 内容 |
|------|------|------|
| `internal/pluginslot/workspace.go` | ~120 | Workspace 接口（Create/Restore/Destroy）+ WorkspaceConfig/WorkspaceInfo（含 RepoPath/Isolation）+ ErrWorkspaceDirty/NotFound/BranchCheckedOutElsewhere/GitUnavailable + ValidateManagedPathForTest 白盒 |
| `internal/pluginslot/workspace_directory.go` | ~170 | DirectoryWorkspace：纯目录隔离 `{root}/{proj}/{sess}` + validatePathComponent sanitize（`..`→`__`）+ validateManagedPath 防 traversal（EvalSymlinks 展开 8.3 短名 + resolveLongPath 逐级祖先展开）+ Destroy 空目录才删 |
| `internal/pluginslot/workspace_git.go` | ~380 | GitWorkspace：os/exec git worktree add -b（branch 复用 + single --force 容 stale）+ remove（不带 --force，dirty→ErrWorkspaceDirty）+ list --porcelain 解析 + GIT_TERMINAL_PROMPT=0 + runGit stdout+stderr 合并（dirty 分类）+ pathEqual 路径归一化 + ForceDestroy（--force+RemoveAll 兜底） |
| `internal/pluginslot/workspace_worker.go` | ~90 | init() 自动注册 directory + git-worktree 两 Factory（RegisterWithManifest + git detect）+ RegisterWorkspaceFactories 幂等恢复 |
| `internal/pluginslot/workspace_directory_test.go` | ~230 | 8 case：Create/Destroy/Dirty 拒绝/Restore 幂等/traversal sanitize/Escape 路径拒绝/orchestrator 后缀/NotFound + SlotWorkspace 注册 4 case |
| `internal/pluginslot/workspace_git_test.go` | ~230 | 8 case 全真实 git 仓：建 worktree+branch/Destroy/Dirty 拒绝（未提交工作保留）/Restore 复用/traversal/ForceDestroy 清理/GitUnavailable/porcelain 解析/超时取消 |

### 机制 1：编排守护 daemon

| 文件 | 行数 | 内容 |
|------|------|------|
| `internal/orchestrator/daemon.go` | ~290 | Daemon：Start(ctx) ticker goroutine（幂等，ctx+Stop 双通道退出）+ Poll（拉事实→DeriveStatus→diff 快照→emit Action）+ ActionKind（nudge/timeout/terminated/status_changed）+ StatusProvider 接口注入 + emit panic 隔离 + 消失 worker→terminated + Snapshot 副本 |
| `internal/orchestrator/daemon_test.go` | ~280 | 9 case：首观察/无变化不重发/转移→nudge/消失→terminated/no_signal 超时/Start-Stop 生命周期幂等/handler panic 隔离/provider 错误传播/快照副本 |
| `internal/orchestrator/e2e_test.go` | ~290 | **三能力联动 E2E**（TestE2E_WorkerIsolationToDaemonToCDC） |

### 台账

| 文件 | 变化 |
|------|------|
| `workflow_status.md` | 追加 AO 批次契约/验收表/验证日志（其他批次内容未动） |

**接口/数据/配置/依赖变化**：
- 接口：新增 `pluginslot.Workspace`（SlotWorkspace 槽位首次有真实 Factory）；`eventstream.Store` 新增 SQLite 实现；`orchestrator.StatusProvider`/`ActionHandler` 可注入
- 数据：新表 `change_log`（独立于主库表，由调用方传入 DB 决定落哪个库；不自动迁移主库 schema）
- 配置：零新增配置段（app 接线留待禁区释放）
- 依赖：**go.mod 零改动**（git 走 os/exec，sqlite 走已有 mattn/modernc，watcher 走 time.Ticker）

## 三、验证（全部实际运行）

| 验证 | 命令 | 结果 |
|------|------|------|
| 编译 | `go vet`（5 包）+ `go build` | EXIT=0 × 全部 |
| 单测 | `go test -count=1 ./internal/statusboard/ ./internal/statusboard/cdc/` | ok（4+6 case） |
| 单测 | `go test -count=1 ./internal/eventstream/`（含 CGO 真 SQLite） | ok（7 case + 原有测试） |
| 单测 | `go test -count=1 ./internal/pluginslot/`（含 16+ 次真实 git 子进程） | ok（12+ case，9.3s） |
| 单测 | `go test -count=1 ./internal/orchestrator/` | ok（9 case） |
| **E2E** | `go test -count=1 -run TestE2E_WorkerIsolationToDaemonToCDC -v` | **PASS 1.45s**：真实 git worktree × 2（directory+git 双模式）→ daemon 推进 2 轮（首观察 working → waiting_input 转 needs_input nudge）→ CDC 事件 SQLite 持久化 ≥3 行 → 看板订阅者投影同步更新 → 重启恢复 cursor → worktree 注册表清理验证 |
| 回归 | `go test -count=1` 5 包全新跑 | 全 ok |

**未运行的验证**：
- `-race`：当前环境 CGO 环境下 `go test -race` 报 "requires cgo enable"（环境限制，非代码问题）；race 安全性靠代码审查（mu 锁两段式读取、RWMutex、atomic）+ 后续 CI 矩阵补充
- app.go 全链路接线：按用户决策留待禁区释放（本期纯新增包自管理生命周期）

## 四、关键设计决策与坑

1. **触发器 vs 显式 Append**：参考项目 CDC 靠 SQLite AFTER 触发器零 emit 代码；CyberStrikeAI 状态事实分散多表（workflow_runs/batch_tasks/c2_sessions），触发器需逐表定制，故改显式 Append（EventStream.AddEvent 分配 ID → handler 显式 store.Append），cause 链一等公民保留。
2. **Windows 8.3 短名坑**：t.TempDir 返回 `ADMINI~1.DES` 短名，git 输出长名 → pathEqual/validateManagedPath 双侧 EvalSymlinks 展开（不存在路径逐级向上找存在祖先展开）。
3. **分发与持久化解耦**：AddEvent 内部 Append 在 ID 分配前会撞 UNIQUE(seq=0)——E2E 用 nil store 纯分发 + 显式 Append（id 回写后），语义更清晰。
4. **同包 Reset 影响**：registry_test.Reset() 清掉 init() 注册 → RegisterWorkspaceFactories 幂等恢复供测试调用。
5. **dirty 保护**：git worktree remove 不带 --force，dirty 时转 ErrWorkspaceDirty 保留 agent 未提交工作；ForceDestroy 仅显式调用才 --force+RemoveAll。

## 五、剩余风险与后续

1. **AO-Audit 审计中**：独立 Critic 正在审计（伪实现/正确性/安全/边界/测试缺口 + 实际跑测试），结果待回报；发现 P0/P1 将修复复验。
2. **app.go 接线未做**（用户决策）：daemon/看板的生产启动需 app.go 追加 `orchestrator.NewDaemon(...).Start(ctx)` + change_log 落库；当前包自管理生命周期，测试驱动验证。禁区（J4/J5 并发会话占用 app.go）释放后可 15 行内接完。
3. **race 检测未跑**：环境 CGO 限制；建议 CI 加 `go test -race ./internal/orchestrator/ ./internal/statusboard/...`。
4. **看板 HTTP 端点未做**：ColumnFor/StatusProvider 已就绪，HTTP SSE 层属 app 接线范围，留待下期。
