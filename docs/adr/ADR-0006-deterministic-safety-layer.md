# ADR-0006 确定性安全层叠在 LLM 之前

**状态**：accepted  
**日期**：2026-09

## 背景

LLM-authored 命令/工具调用存在注入风险（`; rm -rf /`、`$(id)`、越界扫描、破坏性操作）。仅靠 RBAC 单道不够，需纵深防御。参考项目（Pentest-Swarm-AI/mcpstrike/VulnClaw/strix）共性：**确定性安全层叠在 LLM 之前，无 LLM 在环**。

## 决策

**五闸纵深防御，全部确定性纯函数，叠在 LLM 推理之前：**

1. **shellsafe**：executor shell exec 路径前过 `ShellSafeParse`，拒绝引号外元字符（`| > < & ; ` $( 换行`）。移植自 Pentest-Swarm-AI。
2. **scope validator**：CIDR/Domain/Port/Excluded 四元，网络工具调用前统一闸门（I 批次落库函数+单测，接入点按需）。移植自 Pentest-Swarm-AI。
3. **HIGH_IMPACT 审批集**：破坏性工具（exec/delete-file/sqlmap/metasploit 等）标记风险，RBAC 之外第二道闸（I 批次落集+单测）。移植自 mcpstrike。
4. **TurnToolCallLimiter + tool_call_ids**：单 turn 工具调用数上限防退化卡死 + tool-call id 唯一防 strict provider 拒整轮。移植自 strix。
5. **project scope_json 会话级授权边界（J4 增补）**：`projects.scope_json` 的 targets/exclude 解析为可硬拦的 `security.Scope`（`internal/security/scope_block.go`），executor 对 MCP 工具、Eino execute guard 对内置 execute 工具统一校验；与工具 yaml scope 叠加（AND 语义），越界目标返回 IsError 不执行。前端/后端对 scope_json 做 fail-closed 结构校验（`validateScopeJSON`），非法配置报 400 而非静默退化为无限制。

## 备选方案对比

| 方案 | 优点 | 劣势 |
|------|------|------|
| **确定性五闸（选定）** | 无 LLM 在环（零延迟零 token）、纯函数可单测、纵深防御 | 误报需人工复核 |
| 仅 RBAC | 最简 | 破坏性操作单道闸，无注入防护 |
| LLM 审批每步 | 灵活 | 耗 token + 延迟 + LLM 可被绕过 |

## 后果

- **正面**：LLM 命令注入被 shellsafe 拦在 executor 前；破坏性操作有第二道闸；退化生成上百次 poll/wait 被 TurnLimiter 截断。
- **负面**：shellsafe 严格可能误拒合法命令（需 `sh -c "..."` 显式包裹）；HIGH_IMPACT 集需持续维护。
- **已知妥协**：CSP `script-src 'unsafe-inline'`（前端 265 处 inline onclick，strict CSP 需先迁移 onclick，属 P2 单独批次）。
- **已知妥协（J4/J5 增补，2026-09-02 Critic 终审披露）**：
  1. project scope_json 是**网络目标语义**（CIDR/域名/端口），不约束文件写入路径——write_file/modify-file 可写进程权限可及的任意路径，仅受 HIGH_IMPACT 标记闸 + HITL 管控。
  2. Eino `edit_file` **不走** capability provider（其 old_string/new_string 参数语义与 provider 的整文件 content 写入不兼容，经 provider 会清空文件——Critic P0 修复），走原生 Edit 语义，破坏性由 HITL/HIGH_IMPACT 管控。
  3. Eino `write_file` 经 provider 时要求父目录已存在（Validate 校验），较原生 MkdirAll 自动建多级目录**收紧**——模型需先 mkdir 或写到已存在目录。
  4. 非法 scope_json 的读取侧 fail-open（解析失败视为无限制），写入侧 fail-closed（API 400）——直改数据库可绕过写入侧校验（低风险，需 DB 访问权限）。
- **设计思想迁移原则**：参考项目的 Python 逻辑（VulnClaw/strix/mcpstrike）用 Go 重写为纯函数，不搬 Python 代码——"逻辑各语言相通，重构而非依赖"。
