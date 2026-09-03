# SOP：新增/修改安全闸与破坏性工具生命周期（J4/J5 模式复用）

> 适用场景：给 CyberStrikeAI 新增"授权范围类硬闸"（如新的 scope 维度）或"破坏性工具生命周期"（如新的 Capability Provider）。
> 本 SOP 从 J4（project scope_json 硬闸）/J5（modify-file Capability Provider）实现中提炼，按序执行可避免重复踩坑。

## 0. 前置阅读（AI 记忆点优先）

1. 读 `docs/adr/ADR-0006-deterministic-safety-layer.md`（确定性五闸总览）——若本 SOP 与 ADR 冲突，以 ADR 为准并更新本 SOP。
2. 读 `workflow_status.md` 中最近一次"终局闭环修复批次"验证日志——确认已知的坑与边界披露，避免重复排查。
3. 读本文第 4 节"已知坑清单"——这些坑都已真实踩过并有测试回归覆盖。

## 1. 需求拆解（先思考后编码）

| 问题 | 判定 |
|------|------|
| 新闸拦截的目标是什么？（网络目标 / 文件路径 / 命令行） | 决定复用 `Scope.Allows`（网络）还是写新校验器 |
| 生效范围？（MCP 工具 / Eino 内置工具 / 两者） | 两者 = 需要两条注入链路（见第 2 节） |
| fail-open 还是 fail-closed？ | 授权边界类一律 fail-closed：配置非法 → 报错拒执行，不静默放行 |
| 是否有配置入口？ | 有 → 后端 API 校验 + 前端结构校验双闸（参考 `validateScopeJSON` + `projects.js`） |

## 2. 两条工具执行链路（J4 核心发现，勿再重查）

```
链路 A（MCP 工具）：handler → agent.ExecuteMCPToolForConversation（注入 WithMCPProjectID）
  → executeToolViaMCP → mcpServer.CallTool → executor.ExecuteTool（project scope 硬闸在此）
链路 B（Eino 内置 execute/write_file 等）：
  runner.go / eino_single_runner.go（传 ProjectID 给 args）
  → runEinoADKAgentLoop（注入 WithMCPProjectID 到 ctx）   ← 此注入点曾缺失，见坑 2
  → einoStreamingShellWrap.ExecuteStreaming（scopeGuard.CheckExecute）
  → einoAgenticFilesystemToolMiddleware.WrapInvokableToolCall（capGuard.CheckFilesystemTool）
```

**新增闸时必须两条链路都覆盖**，否则 Eino 内置工具（execute/write_file/edit_file）会绕过闸。

## 3. 实施步骤

### 3.1 新增授权范围硬闸
1. 解析器：`internal/security/scope_block.go` 加 `XxxFromProject(db, projectID)`（nil-safe：db/projectID 空 → 零值=不限）。
2. executor 侧：`internal/security/executor_run.go` ExecuteTool 加校验段（放在 capability 分支**之前**，越界早退）。
3. Eino 侧：`eino_execute_streaming_wrap.go` scopeGuard 调用点加新校验；越界 hint 必须加 `einomcp.ToolErrorPrefix`（模型面 IsError 一致，见坑 4）。
4. 配置入口校验：`internal/handler/` 加 `validateXxx`（fail-closed），Create/Update 双挂；`web/static/js/` 对应表单加结构校验。
5. 测试：解析纯函数 case + executor 集成（越界拦/授权内放行/未绑定 project 放行）+ handler 校验 case。参考 `scope_block_test.go`、`project_scope_test.go`。

### 3.2 新增破坏性工具 Capability Provider
1. `internal/capability/` 新建 provider：实现 `Supports/Plan/Validate/Execute/Rollback/CollectArtifacts`。
   - Execute 必须把可回滚状态（如备份路径）写进 `args["_xxx"]` 或返回文本，供调用方回填 plan。
   - Rollback 必须覆盖"新建"（无备份 → 删除）与"修改"（有备份 → 恢复）两种语义。
2. 注册：`internal/app/app.go` `capability.NewXxxProvider(backupDir)`（backupDir 空值兜底 `tmp/reduction/xxx-backup`，勿落 CWD，见坑 3）。
3. 工具 yaml：`tools/<name>.yaml`，command 用 `internal:capability` 路由 + 完整 parameters 定义（没有 yaml = 工具不可达 = 死代码，见坑 1）。
4. Eino 内置工具映射（若适用）：`internal/multiagent/filesystem_capability_guard.go` `resolveFilesystemProvider` 加映射。
5. 测试：完整生命周期（修改+新建+回滚+工件）+ guard 映射 case。参考 `provider_test.go`、`filesystem_capability_guard_test.go`。

## 4. 已知坑清单（每条都有回归测试或验证日志背书）

| # | 坑 | 症状 | 正确做法 |
|---|-----|------|---------|
| 1 | capability 分支死代码 | provider 注册了但无工具 yaml → ExecuteTool toolIndex 查不到 → GetProvider 永不可达 | provider 注册必须配对 `tools/*.yaml` |
| 2 | Eino ctx 无 projectID | scopeGuard 构造了但 CheckExecute 读 ctx projectID 恒空 → 恒放行 | runEinoADKAgentLoop 入口注入 `mcp.WithMCPProjectID`（runner 传 args.ProjectID） |
| 3 | 备份目录落 CWD | `filepath.Join("", "capability-backup")` = 相对路径 → CWD 不可写时全挂 | 空值兜底 `os.TempDir()/reduction/...` |
| 4 | 越界提示不带错误标记 | Eino 流结果无 `ToolErrorPrefix` → 模型面不认为失败 | hint 加 `einomcp.ToolErrorPrefix` 前缀 |
| 5 | plan.BackupPath 断链 | Plan 时备份还不存在 → Rollback 拿空路径 | Execute 写 `args["_backup_path"]`，调用方回填 plan |
| 6 | 新文件被 Validate 误拦 | Validate 要求文件存在 → write_file 创建语义 100% 失败 | 不存在视为 create：校验父目录；Execute MkdirAll |
| 7 | fail-open 配置校验 | scope_json 非法 JSON → 解析返回零值 → 静默无限制 | handler/前端双闸 fail-closed（400 拒绝） |
| 8 | Windows 测试 TempDir 句柄 | sqlite 测试 cleanup 失败误报 FAIL | 测试尾部 `defer db.Close()` |
| 9 | 本机 CGO 不可用 | sqlite 测试全 skip/fail | `CGO_ENABLED=0 go test -tags='sqlite_pure_go'`；CI 走 CGO_ENABLED=1 |
| 10 | **Eino edit_file 映射到整文件写入 provider** | edit_file 参数是 file_path/old_string/new_string（无 content 键），经 modify-file provider 会把 content 缺省为空串 → **整文件被清空且假成功**（Critic 终审 P0） | edit_file 不映射 provider，走原生 Edit 语义；只有 write_file（file_path/content 整文件语义）映射。映射前必须核对 Eino 工具的真实参数键名 |

## 5. 验收清单（缺一不可）

- [ ] `CGO_ENABLED=0 go build -tags='sqlite_pure_go' ./...` 全量 exit 0
- [ ] `go vet`（改动包）exit 0
- [ ] 新增闸/生命周期测试全 PASS（含正常/越界/新建/回滚/nil-safe/fail-closed）
- [ ] 两条链路（MCP + Eino）grep 确认都接入
- [ ] 文档同步：ADR（闸数量/决策）、README 安全段、config.example.yaml 示例、spec.md/todo.md 状态
- [ ] `workflow_status.md` 验证日志追加（含披露边界）
- [ ] 生产二进制重建（`go build -o server.exe ./cmd/server`）

## 6. 记忆点（下次会话优先读取）

- 本 SOP 位置：`docs/sop/safety-gate-capability-sop.md`。改安全闸/破坏性工具前先读本文件第 4 节。
- J4/J5 最终状态以 `workflow_status.md` 验证日志为准（含日期），不要凭 spec.md 旧状态推断。
- executor 已拆分为 `executor_policy.go`（注入面）/`executor_run.go`（ExecuteTool 主流程）/`executor_build.go`（参数构建）；改 ExecuteTool 逻辑去 executor_run.go。
