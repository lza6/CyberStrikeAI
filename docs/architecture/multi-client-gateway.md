# 多客户端 Context Gateway 架构

> 状态：已验证（现状对齐）+ 合理推断（待落地接缝）。遵循 spec-driven-development 工作流。
> 来源：context-mode 参考项目（18/25）机制 4「多客户端 + OpenClaw gateway」迁移分析。
> 日期：2026-09-02。

## 一、设计意图

CyberStrikeAI 已天然是「一个 context/state 服务喂多前端」的形态：所有客户端
（Web SSE / 机器人 / 批量任务 / 桌面）最终都汇聚到 Eino 多代理编排入口
`runEinoADKAgentLoop`。本文档把这一现状显式化，并补强并发会话隔离与状态持久化，
作为 context-mode 四项迁移的第 4 项（优先级=中）。

## 二、现状（已验证）

| 前端 | 入口 | 鉴权 | 状态隔离机制 |
|------|------|------|-------------|
| Web SSE | `/api/eino-agent/stream`、`/api/multi-agent/stream` | Bearer Token + RBAC | conversation_id（SQLite 行级） |
| 企微/钉钉/飞书/Telegram/Slack/Discord/QQ | `robot_default_agent_mode`（`config.example.yaml:271`）| user_binding / service_account | robot session → conversation_id 映射 |
| 批量任务 | `/api/batch-task/*` | RBAC | 每子任务独立 conversation_id |
| 桌面 Electron | 同 Web SSE（local_mode 免登录）| local_mode admin | 同 conversation_id |

汇聚点：`runEinoADKAgentLoop`（`internal/multiagent/runner.go`，所有路径的公共执行点）。

## 三、与 context-mode gateway 的差距

| 维度 | context-mode | CyberStrikeAI 现状 | 差距 |
|------|-------------|-------------------|------|
| 多租户隔离 | per-project SQLite（`<hash>.db`）| 单 SQLite + conversation_id 行级 | 无 per-project DB；靠行级 + RBAC |
| session 稳定主键 | `session_key`（"agent:name:main"）扛重启 | `conversations.last_react_*`（`config.example.yaml:318` 注释明说跨轮模型态走此） | 部分有 |
| 多客户端识别 | MCP 握手 `clientInfo.name` → adapter | 无（所有前端走同一 SSE 协议） | 无需（前端同构） |
| workspace 路由 | `WorkspaceRouter` 从参数反解 workspace | `workspace_root_dir` + `projects/{id}` 或 `conversations/{id}`（`config.example.yaml:132`） | 已有等价 |
| 插件 in-process | OpenClaw plugin 跑在 gateway 进程内 | Eino middleware in-process（`eino_middleware`） | 已有等价 |

## 四、落地建议（合理推断，待授权）

1. **状态隔离强化**：conversation_id 行级隔离已足够单机场景；per-project DB 是
   context-mode 的多租户优化，CyberStrikeAI 单机部署无此需求，**不迁移**。
2. **稳定主键**：`conversations.last_react_input/last_react_output`（`database.go:181-190`）
   已承担跨轮模型态锚点，等价于 context-mode 的 `session_key`。**无需新增表**。
3. **workspace 路由**：`workspace_root_dir` 已按 `projects/{id}` / `conversations/{id}`
   隔离（`config.example.yaml:132`），等价于 `WorkspaceRouter`。**无需新增**。
4. **多客户端识别**：所有前端同构走 SSE，无需 adapter 层。**无需新增**。

## 五、结论

第 4 项「多客户端 gateway」在 CyberStrikeAI 已天然满足：单进程 + 多前端汇聚 +
conversation_id 隔离 + workspace 路由。context-mode 的 per-project DB 多租户优化
是分布式场景需求，单机桌面/本地部署无需迁移。**本项标记为「已验证-现状满足」，
不实施代码改造**，仅以此文档显式化现状。

## 六、证据

- `internal/multiagent/runner.go`：`runEinoADKAgentLoop` 公共执行点
- `config.example.yaml:132`：`workspace_root_dir` 按 projects/conversations 隔离
- `config.example.yaml:271`：`robot_default_agent_mode` 机器人汇聚
- `config.example.yaml:318`：`checkpoint_dir` 注释「跨轮模型态统一走 conversations.last_react_*」
- `internal/database/database.go:181-190`：conversations 表行级隔离
