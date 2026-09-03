# CyberStrikeAI Spec-Driven Development

> 先规范后编码。本目录为 8 批次（K0a/K0b/K0c/K3/K4/K8/K9/K10）的回溯 spec 索引。
> 基于 [github/spec-kit](https://github.com/github/spec-kit)（Spec-Driven Development 工具包）+ [addyosmani/agent-skills](https://github.com/addyosmani/agent-skills)（spec-driven-development skill）方法论。

---

## 1. spec-kit 是什么（联网真实查证）

### 1.1 github/spec-kit（官方工具包）

- **仓库**：https://github.com/github/spec-kit （133k+ stars，2026-09-02 发布 v1.0.4）
- **描述**："Toolkit to help you get started with Spec-Driven Development" — 开源工具包，把规范变成可执行流程
- **核心工具**：`specify-cli`（Python，经 `uv` 安装）—— 生成 spec/plan/tasks 三件套，配合 `/speckit-*` slash 命令驱动 AI 编码代理
- **安装**：`uv tool install specify-cli --from git+https://github.com/github/spec-kit.git@v1.0.4` 或 `uv tool install specify-cli`（PyPI）
- **工作流**：`/speckit.constitution`（建项目原则）→ `/speckit.specify`（写规范）→ `/speckit.plan`（规划）→ `/speckit.tasks`（拆任务）→ `/speckit.implement`（实现）→ `/speckit.converge`（收敛验证，重复直到 Converged）
- **核心理念**：规范是可执行的（specifications become executable），不是被丢弃的脚手架

### 1.2 addyosmani/agent-skills 的 spec-driven-development skill

- **仓库**：https://github.com/addyosmani/agent-skills （91k+ stars，2026-09-03 更新）
- **描述**："Production-grade engineering skills for AI coding agents" — 25 个技能包，定义→规划→构建→验证→审查→发布全生命周期
- **spec-driven-development skill**：写 PRD 覆盖 objectives / commands / structure / code style / testing / boundaries，**编码前先写规范**
- **SKILL.md 解剖**：Frontmatter（name + description + Use when）→ Overview → When to Use → Process（步骤化工作流）→ Rationalizations（反借口表）→ Red Flags → Verification（证据要求）
- **安装**：`npx skills add addyosmani/agent-skills` 或 `/plugin marketplace add addyosmani/agent-skills`

### 1.3 spec-kit 与 agent-skills 的关系

两者独立但互补：spec-kit 是 GitHub 官方的工具链（CLI + slash 命令），agent-skills 是 Addy Osmani 的技能包（SKILL.md 工作流）。CyberStrikeAI 采用 agent-skills 的 SKILL.md 规范格式 + spec-kit 的 spec/plan/tasks 三件套思想，**不引入 specify-cli 工具依赖**（避免 Python 工具链污染 Go 项目）。

---

## 2. 本地技能检查结果

| 位置 | 技能 | 用途 |
|------|------|------|
| `~/.claude/skills/speckit-orchestrator/SKILL.md` | speckit-orchestrator | SDD 工作流编排（spec→plan→task 协调） |
| `~/.claude/skills/spec-writing/SKILL.md` | spec-writing | 从特性描述写规范（用户故事 + 验收标准） |
| `~/.claude/skills/042-planning-openspec/` | openspec | 规划 + OpenSpec 集成 |
| `~/.claude/skills/graft/SKILL.md` | graft | 代码图谱（项目级 `.claude/skills/graft/`） |

**结论**：本地已有 spec-driven 相关技能（speckit-orchestrator + spec-writing + openspec），无需新装。本次回溯 spec 直接采用 agent-skills 的 SKILL.md 规范格式，不引入 specify-cli 工具依赖。

---

## 3. 8 批次 Spec 文档索引

| 批次 | Spec 文件 | 主题 | 状态 |
|------|-----------|------|------|
| K0a | [K0a-vertical.md](K0a-vertical.md) | 垂直域抽象奠基（vertical interface + security 首实现） | done |
| K0b | [K0b-blackboard-sqlite.md](K0b-blackboard-sqlite.md) | 黑板 SQLite 持久化（WAL + FTS5 降级 + 双驱动适配） | done |
| K0c | [K0c-skillpackage-recursive.md](K0c-skillpackage-recursive.md) | skillpackage 递归扫描（WalkDir + name 语义） | done |
| K3 | [K3-quality-gates.md](K3-quality-gates.md) | 质量门（-race + cover + gitleaks + govulncheck） | done |
| K4 | [K4-unified-home.md](K4-unified-home.md) | 统一 home 目录默认接入（~/.cyberstrikeai/） | done |
| K8 | [K8-sarif-attackchain.md](K8-sarif-attackchain.md) | 安全深化（SARIF 2.1.0 + 攻击链 + CWE 归一化） | done |
| K9 | [K9-orchestration-strategy.md](K9-orchestration-strategy.md) | 编排策略层（StuckDetector + reactions lifecycle + retry/backoff） | done |
| K10 | [K10-engineering-ci.md](K10-engineering-ci.md) | 工程化 CI 矩阵（golangci v2 + gofmt gate + PR 风险分级） | done |

每个 spec 含 8 段：Objective / Tech Stack / Commands / Project Structure / Code Style / Testing Strategy / Boundaries（Always/Ask first/Never）/ Success Criteria + Open Questions。

---

## 4. agent-skills SKILL.md 规范提炼

```
SKILL.md 解剖（来自 addyosmani/agent-skills）：
┌─────────────────────────────────────────────────┐
│ Frontmatter                                      │
│   name: lowercase-hyphen-name                    │
│   description: Guides agents through [task].     │
│                Use when…                         │
├─────────────────────────────────────────────────┤
│ Overview         → What this skill does           │
│ When to Use      → Triggering conditions          │
│ Process          → Step-by-step workflow          │
│ Rationalizations → Excuses + rebuttals（反借口表）│
│ Red Flags        → Signs something's wrong        │
│ Verification     → Evidence requirements          │
└─────────────────────────────────────────────────┘
```

**关键设计选择**：
- **Process, not prose**：技能是工作流（步骤+检查点+退出标准），不是参考文档
- **Anti-rationalization**：每个技能含反借口表（如"我稍后加测试"）+ 反驳论据
- **Verification is non-negotiable**：每个技能以证据要求结尾（测试通过/构建输出/运行时数据），"看起来对"永远不够
- **Progressive disclosure**：SKILL.md 是入口，支持 references/ 按需加载

**目录结构**：
```
agent-skills/
├── skills/<name>/SKILL.md          # 技能入口（frontmatter + 6 段）
├── skills/<name>/references/       # 补充检查清单（按需加载）
├── skills/<name>/scripts/          # 可选脚本
├── agents/                          # 专家 persona
├── references/                      # 仓库级共享检查清单
└── .claude/commands/                # slash 命令
```

---

## 5. Spec-Driven Development 工作流（先规范后编码）

### 5.1 新批次工作流

1. **Specify**：在 `docs/spec/` 写 spec（Objective/Tech Stack/Commands/Structure/Code Style/Testing/Boundaries/Success Criteria）
2. **Plan**：拆任务节点（每节点有交付物 + 验证 + 完成标准），记入 `workflow_status.md`
3. **Implement**：TDD（先写测试 RED → 实现 GREEN → 重构 IMPROVE）
4. **Verify**：`go vet/build/test` 双路径 + curl/Playwright 真实链路证据
5. **Review**：code-reviewer / security-reviewer 代理审查
6. **Converge**：对照 Success Criteria 逐条终验，达标才标 done

### 5.2 改代码前判断 spec 是否过时流程（CRITICAL）

AI 下次改代码前**必须**执行：

1. **先读 `docs/spec/`**：找到对应批次的 spec 文件（如改 `internal/vertical/` → 读 `K0a-vertical.md`）
2. **对照 Success Criteria**：检查 spec 标注的"done"项是否仍成立（grep 源码 + 跑测试）
3. **判断是否过时**：
   - 若源码与 spec 的 Project Structure / Code Style / Boundaries 不一致 → spec 过时，先更新 spec 再改代码
   - 若源码与 spec 一致 → spec 有效，按 spec 的 Boundaries（Always/Ask first/Never）约束改动
4. **改代码后同步 spec**：若改动影响 Project Structure / Success Criteria / Open Questions → 同步更新 spec 文件
5. **记入 workflow_status.md**：只记事实与证据，只有观察到交付物才标 done

### 5.3 Boundaries 三层（每个 spec 强制段）

- **Always**：每次改动必须做的（写测试再实现、go vet/build 过了才算完、config.example.yaml 同步）
- **Ask first**：需用户确认的（改接口签名、改 RBAC 权限、改 schema 迁移、升级核心框架）
- **Never**：禁止做的（删除现有工具/skill/agent、改 local_mode 语义、硬编码 key、把 Mock 当实现、为通过测试削弱断言）

---

## 6. 相关文档

- 项目级规则：`AGENTS.md`（CLAUDE.md 软链）
- 单一事实源：`workflow_status.md`（节点验收证据）
- I 批次 spec：`spec.md`（参考项目设计迁移）
- 结果计划指南：`参考的结果计划指南.md`（K0-K12 批次规划）
- 文件系统映射：`docs/FILE-SYSTEM-MAP.md`（K10 PR 风险分级器 Tier 映射）

---

*最后更新：2026-09-04 · 基于 github/spec-kit v1.0.4 + addyosmani/agent-skills（2026-09-03）*
