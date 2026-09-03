# OpenHands 三特性迁移落地 · 变更报告

> 本批次落地 OpenHands-main 的 3 个高优先级特性到 CyberStrikeAI（Go）。
> 全部为**全新独立 leaf 包**，不触碰现有文件，不与其他会话冲突。
> 验证：go vet/build/test 全过；真实 E2E 链路验证通过。

## 交付物

| 特性 | 新包 | 对应 OpenHands 源 | 状态 |
|------|------|------------------|------|
| microagent 可插拔上下文单元 | `internal/microagent/` | `openhands/microagent/*` | ✅ 已验证 |
| EventStream pub/sub + Recall 一等公民 | `internal/eventstream/` | `openhands/events/stream.py` + `events/action/agent.py` | ✅ 已验证 |
| Prompt assembly as struct | `internal/promptassembly/` | `openhands/utils/prompt.py` | ✅ 已验证 |
| E2E 集成验证 | `internal/integration/` | 三特性协同链路 | ✅ 已验证 |

## 一、internal/microagent/（可插拔上下文单元）

移植自 OpenHands `openhands/microagent/microagent.py` + `types.py`。

### 文件
- `types.go` — `MicroagentType`（knowledge/repo/task）、`Metadata`、`InputMetadata`、`Knowledge`
- `agent.go` — `Microagent` struct + `MatchTrigger`（小写 substring 匹配）+ `inferType`（inputs/triggers 推断）
- `loader.go` — `LoadFromDir`（frontmatter 解析，复用 `skillpackage/frontmatter.go` 的 `---` 拆分模式 + yaml.v3）
- `registry.go` — `Registry`（三层覆盖 global→user→repo、禁用名单、按会话去重 `perConversationSeen`）
- `render.go` — `RenderExtraInfo`/`RenderRepoInstructions`（对应 `microagent_info.j2` / `additional_info.j2`）
- `microagent_test.go` — 10 个测试用例

### 关键行为
- 三类型：knowledge（关键词触发）/ repo（always-on）/ task（`/name` 触发，自动追加 trigger）
- 三层覆盖：后加载者同名覆盖先加载（移植自 `memory.py:282-286`）
- 关键词匹配：`strings.Contains(lower(msg), lower(trigger))`，命中第一个（移植自 `microagent.py:189-199`）
- 按会话去重：`perConversationSeen[convID][name]` 避免跨轮重复注入（移植自 `conversation_memory.py:711-757`）
- 禁用名单：`SetDisabled([]string)` 运行时过滤（移植自 `AgentConfig.disabled_microagents`）

## 二、internal/eventstream/（typed event bus + Recall 一等公民）

移植自 OpenHands `openhands/events/stream.py` + `events/action/agent.py` + `events/observation/agent.py`。

### 文件
- `event.go` — `Event` 接口 + `BaseEvent`（id/timestamp/source/cause）+ `EventSource` 枚举 + `SubscriberID` 枚举
- `action.go` — `RecallAction`/`RecallObservation`/`CondensationAction`/`MicroagentKnowledge` + `RecallType`
- `stream.go` — `EventStream`（pub/sub + 单调递增 ID + cause 链 + 防回环）
- `store.go` — `Store` 接口 + `Filter`（移植自 `event_store_abc.py` + `event_filter.py`）
- `memory_store.go` — `MemoryStore`（进程内 Store 实现，测试/轻量场景）
- `eventstream_test.go` — 8 个测试用例（含 cause 链、单调 ID、订阅者顺序、持久化恢复、防回环）

### Go vs Python 实现差异
- Python 用 `queue.Queue` + `ThreadPoolExecutor(max_workers=1)`；Go 用 `chan Event`（buffered）+ 每订阅者一个 goroutine
- 每订阅者专属 goroutine 保证"同订阅者顺序、跨订阅者并行"
- 控制事件（Recall/Condensation）用阻塞投递（select 无 default）保证不丢；高频 delta 不走本总线
- 防回环：`ev.ID() > 0 && ev.ID() != INVALID_ID` 拒绝已分配 ID 的事件再发布

### 关键行为
- `AddEvent(ev, src, cause)`：分配单调递增 ID → 打时间戳 → 持久化（若 Store 注入）→ 广播到所有订阅者
- `cause` 链：RecallObservation.Cause = RecallAction.ID，移植自 `agent_controller.py:560-570` 的 pending_action 匹配
- `Subscribe(subID, cbID, buf, cb)` 返回 cancel 闭包；同 (subID, cbID) 重复报错（移植自 `stream.py:144-148`）
- `Close()` 释放所有订阅者 goroutine
- `GetEventByID(id)` 从 Store 检索（供 cause 链回溯）

## 三、internal/promptassembly/（Prompt assembly as struct）

移植自 OpenHands `openhands/utils/prompt.py` 的三 dataclass + `PromptManager`。

### 文件
- `types.go` — `ConversationInstructions`/`RepositoryInfo`/`RuntimeInfo`/`MicroagentKnowledge`/`Context`
- `manager.go` — `Manager`（Go text/template 渲染，内联模板常量，不依赖外部 .j2 文件）
- `promptassembly_test.go` — 7 个测试用例（含全填/空/幂等/条件渲染/microagent 块）

### 关键行为
- 三 struct 字段对齐 OpenHands：`ConversationInstructions.Content` / `RepositoryInfo{RepoName,RepoDirectory,BranchName}` / `RuntimeInfo{Date,WorkingDir,AvailableHosts,AdditionalAgentInstructions,CustomSecretsDescriptions}`
- Go text/template 复刻 `additional_info.j2` 的 `<REPOSITORY_INFO>`/`<RUNTIME_INFORMATION>`/`<CONVERSATION_INSTRUCTIONS>`/`<REPOSITORY_INSTRUCTIONS>` 块
- 条件渲染：字段为空跳过整块（移植自 `additional_info.j2` 的 `{% if %}`）
- 幂等：同一 Context 多次渲染结果一致（planner/replanner 看到一致上下文）
- `RenderMicroagentInfo([]MicroagentKnowledge)` 对应 `build_microagent_info` + `microagent_info.j2`

## 四、internal/integration/（E2E 集成验证）

### 文件
- `e2e_recall_test.go` — `TestE2E_MicroagentRecallToPromptAssembly`
- `helper_test.go` — `writeTempMicroagents` 辅助

### E2E 链路（移植自 OpenHands 完整 Recall 链路）
```
用户消息
  → microagent.Registry.Retrieve 命中（sqli 关键词）
  → 发布 RecallAction 到 EventStream（WORKSPACE_CONTEXT + KNOWLEDGE 两路）
  → Memory 订阅者消费 RecallAction
  → 产出 RecallObservation（cause=action.ID，建立 cause 链）
  → promptassembly.Manager.Render 把 RecallObservation 字段渲染为 prompt 块
```

### 断言
- WORKSPACE_CONTEXT recall 产出含 RepoInstructions + WorkingDir 的 observation
- KNOWLEDGE recall 产出含 sqli microagent 内容的 observation
- xss microagent 不被误触发（消息无 xss 关键词）
- cause 链完整：两个 observation 的 Cause 分别等于对应 action 的 ID

## 验证证据

### 真实运行命令与结果
```
$ go test ./internal/microagent/ ./internal/eventstream/ ./internal/promptassembly/ ./internal/integration/ -count=1
ok  	cyberstrike-ai/internal/microagent	0.090s
ok  	cyberstrike-ai/internal/eventstream	0.064s
ok  	cyberstrike-ai/internal/promptassembly	0.045s
ok  	cyberstrike-ai/internal/integration	0.573s

$ go vet ./internal/microagent/ ./internal/eventstream/ ./internal/promptassembly/ ./internal/integration/
（无输出 = 通过）

$ go build ./internal/microagent/ ./internal/eventstream/ ./internal/promptassembly/
（无输出 = 通过）
```

### 测试用例数
- microagent: 10 个（解析/触发/三层覆盖/去重/禁用/重置/渲染）
- eventstream: 8 个（分配ID/单调/cause链/顺序/持久化/检索/防回环/重复订阅）
- promptassembly: 7 个（全填/空/幂等/条件渲染/microagent块/IsEmpty/日期）
- integration: 1 个 E2E（完整 Recall 链路）

## 与现有代码的边界

- **不修改任何现有文件**：全部新建 `internal/{microagent,eventstream,promptassembly,integration}/` 4 个目录
- **leaf 包**：只依赖标准库 + yaml.v3 + 内部 leaf 类型，不反向导入 agent/handler/project
- **不与其他会话冲突**：已向 cyberstrikeai-0a 发送工作区声明；不触碰 executor.go/config.go/web/js 等
- **与 Eino 衔接预留**：promptassembly.Render 产出字符串后，仍由调用方走 `project.AppendSystemPromptBlock` 接 Eino Instruction；eventstream 可作为 Eino drain 的旁路类型化事件层（高频 delta 不走本总线）

## 剩余风险与待办

1. **接入现有体系（待后续批次）**：三特性当前是独立 leaf 包，尚未接入 `handler/project_context.go` 的 `agentSessionContextBlock` / `multiagent/runner.go` 的 systemPromptExtra 注入点。接入需改现有文件，已声明给其他会话避让，留待下一批次协调。
2. **RecallTypeTask 变量提取**：OpenHands 的 `${variable_name}` 提取（`microagent.py:252-269`）首期未完整支持，`inferType` 已识别 task 类型并自动追加 `/name` trigger，但变量收集需接入 Eino ADK 流程。
3. **SQLite Store 实现**：当前 `MemoryStore` 是进程内实现；生产用 SQLite Store 需新增 `event_stream` 表（id/conversation_id/source/type/payload/timestamp/cause），与现有 `process_details` 表分层（控制事件走 event_stream，高频 delta 走 process_details 聚合）。
4. **Recall 与 Eino run loop 的死锁风险**：OpenHands AgentController 同步等 RecallObservation；CyberStrikeAI 的 Eino run loop 是单 goroutine 消费 `iter.Next()`。接入时必须确保 Memory 订阅者在独立 goroutine（channel 天然分离），且 run loop 不直接阻塞等 RecallObservation，而是让下一次 `iter.Next()` 自然收到回流。
