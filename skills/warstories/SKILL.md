---
name: warstories
description: VulnClaw 实战战报库 — 真实渗透测试/CTF 解题的攻击链记录，含弯路、关键突破口、payload、经验总结；供持续学习与模式复用。
---

# CyberStrikeAI 战报库（War Stories）

本 skill 索引 VulnClaw 的真实渗透测试/CTF 解题战报。每份战报记录完整的攻击链：从信息收集到最终 flag，包括走了哪些弯路、关键突破口在哪里。

## 现有战报

- `2026-04-19_php-deserialization_regex-bypass.md` — PHP 反序列化 + 正则绕过
- `2026-04-19_php-weak-comparison_double-write-md5-bypass.md` — PHP 弱比较 + 双写 MD5 绕过

## 如何写一份新战报

参考 `README.md` 的命名规则与模板：
- 元信息（时间/目标类型/难度）
- 攻击链（侦察→利用→后渗透）
- 关键突破（什么信号让你确认了漏洞）
- 弯路（哪些尝试失败了，为什么）
- Payload（实际可复跑的请求/代码）
- 经验总结（可复用的模式）

## 如何用

- 遇到类似漏洞类型时，先在此库搜历史战报，复用有效 payload 与绕过思路
- 每完成一次授权测试，按模板写一份新战报追加到此目录，沉淀经验
- 与 `skills/pentest-reasoning-loop/`（ReAct 循环方法论）配合：战报是循证素材库
