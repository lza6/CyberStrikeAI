# ADR-0001 选择 Go + Eino ADK 作为 Agent 编排

**状态**：accepted  
**日期**：2026-09

## 背景

CyberStrikeAI 是 AI 原生网络安全平台，需要多 Agent 编排（单代理 + Deep/Plan-Execute/Supervisor 多代理模式）。技术栈选型需平衡：单二进制部署、并发性能、SQLite CGO 集成、生态成熟度。

## 决策

采用 **Go 1.25 + CloudWeGo Eino ADK** 作为 Agent 编排框架。

## 备选方案对比

| 方案 | 优点 | 劣势 |
|------|------|------|
| **Go + Eino ADK（选定）** | 单二进制部署、goroutine 并发、CGO SQLite、Eino 原生多代理/skill/中间件、国产生态 | 生态较 LangChain 小，部分模式需自行适配 |
| Python + LangChain/LangGraph | 生态最大、库最多 | 需 Python runtime、打包复杂、桌面壳额外依赖、CGO 集成弱 |
| Node + Mastra/Vercel AI SDK | 前后端同语言 | 单进程 IO 密集型、SQLite 集成弱、二进制部署难 |

## 后果

- **正面**：单二进制（`cyberstrike-ai.exe`）+ NSIS 桌面安装包，小白双击即用；CGO SQLite WAL 支持生产级写入；Eino 中间件链（90+ 个）提供 budget/限流/反幻觉等纵深防御。
- **负面**：参考项目的 Python 设计思想（VulnClaw/strix/mcpstrike）需 Go 重写，不能直接搬代码；部分 Eino 模式文档需查 CloudWeGo。
- **迁移策略**：设计思想迁移而非代码搬运——确定性 digest、证据双轨、skill 确定性路由等用 Go 重写为纯函数。
