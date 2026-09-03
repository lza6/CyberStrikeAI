---
name: vulnclaw-core
description: VulnClaw 7 个 core skill 的索引（exploitation/pentest-flow/post-exploitation/recon/reporting/vuln-discovery/waf-bypass）；本文件仅作目录导航，每个子目录有独立 SKILL.md。
---

# VulnClaw Core Skills 索引

本目录是 VulnClaw 项目 7 个 core skill 的集合，每个子目录是一个独立的、符合 CyberStrikeAI SKILL.md 标准的 skill：

| 子目录 | 覆盖域 |
|--------|--------|
| `recon/` | 信息收集流程 — 被动+主动侦察 |
| `vuln-discovery/` | 漏洞发现流程 — 基于信息收集结果扫描漏洞 |
| `exploitation/` | 漏洞利用流程 — 构造 PoC 验证和利用已发现漏洞 |
| `post-exploitation/` | 后渗透流程 — 内网信息收集与横向移动 |
| `pentest-flow/` | 渗透测试全流程编排 — 从信息收集到报告生成 |
| `reporting/` | 报告生成流程 — 生成结构化渗透测试报告和 PoC |
| `waf-bypass/` | WAF 绕过技巧库 — 各类 WAF 绕过方法 |

> 每个子目录下的 `SKILL.md` 才是真正的 skill 清单（含 frontmatter `name`+`description`+可选 `routing`）。本文件仅作索引，供浏览用。
