# 文件系统映射表（K10 PR 风险分级器 Tier 映射）

> 本文档定义 `scripts/pr-risk-check.mjs` 对每个 `internal/*` 包的风险分级（tier）映射。
> 分级器基于文件路径关键词匹配，本表是"为什么这样分"的权威说明。
>
> 维护规则：新增 `internal/` 子包时，必须在此表登记 tier，并同步更新 `pr-risk-check.mjs` 的 `RISK_TIERS` 模式。

---

## Tier 定义

| Tier | 标签 | 含义 | 审查要求 |
|------|------|------|---------|
| **critical** | 🔴 critical | 安全/HITL/能力/鉴权——影响授权边界与执行权 | 强制人工审查 + 安全审查 |
| **high** | 🟠 high | 工作流/多代理/处理器——影响任务编排与运行时路径 | 强制人工审查 |
| **medium** | 🟡 medium | 成本/监控/输出——影响可观测性与计量，非阻断主路径 | 常规审查 |
| **low** | 🟢 low | 文档/非生产代码 | 抽查 |
| **config** | ⚙️ config | 配置/构建文件——单独标记，不直接套 tier | 视内容定 |
| **test** | 🧪 test | 测试文件（_test.go / *.spec.js）——降一级处理 | 抽查 |

**Risk level 取最高**：一个 PR 只要有 critical 文件，整体 risk level = critical。

---

## 57 internal 包 Tier 映射

### 🔴 critical（32 包）

影响授权边界、执行权、安全与持久化。这些包改一行都可能放大成安全漏洞或权限提升。

| 包 | 理由 |
|----|------|
| `internal/security` | 安全闸门核心：CSP/shell 二进制/secureheaders/verifier |
| `internal/securityevents` | 安全事件审计流，影响事故取证 |
| `internal/hitl` | 人机回路（Human-in-the-loop）确认门，影响授权执行 |
| `internal/capability` | Capability Provider 生命周期：modify_file 等高危能力 |
| `internal/permissions` | 权限决策器，控制 tool/资源访问 |
| `internal/authctx` | 鉴权上下文 principal，身份与委托链 |
| `internal/ctxsandbox` | 上下文沙箱，隔离不当时泄漏 |
| `internal/attackchain` | 攻击链构建与 truncate，影响操作链路正确性 |
| `internal/c2` | C2（Command & Control）监听/beacon/crypto/payload |
| `internal/metrics` | Prometheus 指标采集，计量正确性 |
| `internal/bounty` | 赏金任务发放与回收 |
| `internal/robot` | 机器人执行体，直接动作执行 |
| `internal/pluginslot` | 插件槽生命周期，挂载点安全边界 |
| `internal/reactions` | 反应引擎，触发链路 |
| `internal/dedup` | 去重，影响审计与限流正确性 |
| `internal/audit` | 审计记录、保留、sanitize、throttle |
| `internal/microagent` | 微代理，子任务执行权 |
| `internal/reasoning` | 推理链，影响决策正确性 |
| `internal/promptassembly` | Prompt 装配，影响注入与越狱风险 |
| `internal/projectprompt` | 项目级 Prompt，影响行为约束 |
| `internal/skillpackage` | 技能包，evals/route_behavior 等行为定义 |
| `internal/einomcp` | Eino MCP 桥接，工具调用边界 |
| `internal/knowledge` | 知识库，检索注入 |
| `internal/storage` | 持久化存储抽象，数据完整性 |
| `internal/vision` | 视觉理解，可能处理敏感图像 |
| `internal/blackboard` | 黑板模式共享状态，并发与可见性 |
| `internal/memdir` | 记忆目录，跨会话状态 |
| `internal/database` | 数据库层，SQL 与迁移 |
| `internal/ctxindex` | 上下文索引（BM25/RRF），检索正确性 |
| `internal/memory` | 记忆融合与分层，provenance/memory_tier |
| `internal/eventstream` | 事件流，SSE/WS 推送 |
| `internal/tooloutput` | 工具输出预算，影响 token 与泄漏 |

### 🟠 high（17 包）

影响任务编排与运行时路径。改这些会让主流程行为变化，但不在授权边界。

| 包 | 理由 |
|----|------|
| `internal/workflow` | Eino 工作流引擎，节点/状态/runner |
| `internal/multiagent` | 多代理协调，filesystem scope guard |
| `internal/orchestrator` | 编排器，任务分发 |
| `internal/swarm` | 群体智能调度 |
| `internal/agent` | Agent 核心，trace/token_counter |
| `internal/agents` | Agent markdown 装配 |
| `internal/agentfinalizer` | Agent 决策终结 |
| `internal/handler` | 请求处理器 |
| `internal/app` | 应用层，app_routes 路由 |
| `internal/playbooks` | 剧本定义 |
| `internal/mcp` | MCP 协议层 |
| `internal/llm` | LLM 抽象 |
| `internal/openai` | OpenAI 适配，SSE sanitizer |
| `internal/einoobserve` | Eino 观测，otlp/metrics |
| `internal/integration` | 集成测试夹具 |
| `internal/statusboard` | 状态看板 |
| `internal/project` | 项目生命周期 |

### 🟡 medium（4 包）

影响可观测性与计量，非阻断主路径。

| 包 | 理由 |
|----|------|
| `internal/cost` | 成本追踪与定价，计量正确性 |
| `internal/monitor` | 监控指标 |
| `internal/termout` | 终端输出格式化 |
| `internal/logger` | 日志层 |

### 🟢 low（4 包）

辅助/配置/入口，非生产核心逻辑。

| 包 | 理由 |
|----|------|
| `internal/config` | 配置加载与校验（虽然影响行为，但属配置层） |
| `internal/cache` | 缓存层 |
| `internal/sarif` | SARIF 报告输出 |
| `internal/vertical` | 垂直领域注册表 |
| `internal/roi` | ROI 计算 |

> 注：`config` / `cache` / `sarif` / `vertical` / `roi` 在 pr-risk-check.mjs 中按 high 处理（因影响运行时行为），此处标 low 是"业务影响"视角。脚本分级以代码路径为准——见 `RISK_TIERS.high.patterns`。

---

## 非 internal 路径 Tier

| 路径 | Tier | 说明 |
|------|------|------|
| `cmd/` | unknown→low | 入口 main，按 low 处理 |
| `docs/` | low | 文档 |
| `scripts/` | low | 辅助脚本 |
| `playbooks/` | low | 剧本资源 |
| `knowledge_base/` | low | 知识库资源 |
| `roles/` | low | 角色定义 |
| `images/` | low | 图片资源 |
| `.github/` | low | workflow 定义（自身改 workflow 也只算 low） |
| `.wolf/` | low | OpenWolf 元数据 |
| `.claude/` | low | Claude 配置 |
| `README*` / `LICENSE` / `SECURITY.md` / `AGENTS.md` / `CLAUDE.md` / `spec.md` | low | 根级文档 |
| `go.mod` / `go.sum` / `config*.yaml` / `Makefile` / `Dockerfile` / `.golangci*.yml` / `.github/workflows/` | config | 构建/配置文件单独标记 |

---

## 测试文件降级规则

以下模式命中的文件归 `test` tier（在取 risk level 时，test 文件的 originalTier 降一级参与计算）：

- `*_test.go`
- `*.spec.[jt]s`
- `*/__tests__/*`
- `*-test.[jt]sx?`
- `web/tests/*`

纯测试 PR → risk level = low。

---

## 与 pr-risk-check.mjs 的对应关系

| 本表 tier | 脚本 RISK_TIERS 键 | patterns 来源 |
|-----------|-------------------|--------------|
| critical | `RISK_TIERS.critical.patterns` | 本表"critical"节列出的所有包路径 |
| high | `RISK_TIERS.high.patterns` | 本表"high"节列出的所有包路径 |
| medium | `RISK_TIERS.medium.patterns` | cost/monitor/termout/logger |
| low | `RISK_TIERS.low.patterns` | docs/scripts/playbooks 等非 internal 路径 |
| config | `RISK_TIERS.config.patterns` | go.mod/go.sum/Makefile 等 |

> **同步铁则**：本表与 `RISK_TIERS` 必须一致。新增包时两处都改，CI 的 pr-risk.yml 会用本表生成的 risk level 决定是否要求强制审查。

---

*K10 工程化 CI 矩阵升级产物 · 对标 agent-orchestrator CI 矩阵*
