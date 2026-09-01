# ADR-0003 MCP 作为工具协议 + 本地工具 yaml 双轨

**状态**：accepted  
**日期**：2026-09

## 背景

平台需支持 100+ 安全工具（nmap/sqlmap/metasploit 等）+ 外部 MCP server 联邦。工具注册需平衡：生态兼容、动态加载、权限控制、供应链安全。

## 决策

**MCP（Model Context Protocol）作为工具协议标准 + 本地 `tools/*.yaml` 声明式工具双轨并行。**

- 本地工具：`tools/<name>.yaml` 声明 name/command/parameters/description，`LoadToolsFromDir` 递归加载（含子目录如 `tools/wireless/`），注册到 MCP 池供 `tools/list` 发现。
- 外部 MCP：stdio/SSE/HTTP transport，`external_mcp_max_concurrent_per_server` 并发限流 + 熔断。
- skill 供应链双闸：`skills-lock.json`（SHA256 锁）+ `verbs-gate`（工具引用漂移门）。

## 备选方案对比

| 方案 | 优点 | 劣势 |
|------|------|------|
| **MCP + yaml 双轨（选定）** | 生态兼容（Cursor/Claude Code 等都能接）、动态加载、权限 RBAC、供应链可锁 | 需维护 yaml schema 一致性 |
| 纯函数调用（Go 注册表） | 最快、类型安全 | 无生态兼容、扩展需改代码、无供应链验证 |
| 纯外部 MCP | 全外部化 | 本地工具也要包成 server，过重 |

## 后果

- **正面**：106+ 工具 yaml + 9 无线工具子目录，启动自动加载；agent 通过 `tools/list` 发现 + `@提及` 引用；skill 锁防投毒，verbs-gate 防 LLM 踩空。
- **负面**：yaml schema 需在 `config.example.yaml` + `tools/README.md` 文档化；新增工具须过 verbs-gate 校验。
- **工具分类**：`tools/wireless/`（无线攻击）、`tools/`（网络/Web/云/二进制等）——`LoadToolsFromDir` 递归支持分类。
