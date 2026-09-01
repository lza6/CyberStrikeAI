---
name: report-templates-bounty
description: 漏洞报告模板：HackerOne / Bugcrowd / Intigriti 三平台差异化格式 + SARIF 2.1.0（GitHub Code Scanning 直接消费）+ FP triage LLM 提示；源自 Pentest-Swarm-AI + agent + deep-eye。
---

# 漏洞报告模板与 FP Triage

## 三平台差异化格式

不同赏金平台对报告结构要求不同，按目标平台选模板：

### HackerOne

```markdown
## Summary
<一句话：什么漏洞 + 影响>

## Steps To Reproduce
1. <curl/HTTP 请求，可复跑>
2. <观察到的响应/指标>
3. <证明影响的下一步>

## Impact
<业务影响：数据泄露/账户接管/...；能量化就量化>

## Severity
CVSS v3.1: <向量>  评分: <X.X>  级别: <Critical/High/Medium/Low>

## CVE / References
- CVE-XXXX-XXXX（如有）
- <官方修复指引/厂商公告链接>
```

### Bugcrowd

```markdown
## Title
<漏洞类型 + 受影响端点>

## Description
<漏洞类概述 + 根因>

## Affected URL
<precise URL>

## Proof of Concept
<可复跑的 HTTP 请求 + 响应证据>

## Severity (Bugcrowd VRT)
<VRT 分类 + 级别>

## Remediation
<修复建议>
```

### Intigriti

```markdown
## Summary
## Impact
## Steps to reproduce
## PoC
## Suggested fix
## Severity (CVSS v3.1)
```

## SARIF 2.1.0（GitHub Code Scanning / Azure DevOps 直接消费）

把 `record_vulnerability` 的发现导出为 SARIF，CI 可自动告警：

```json
{
  "version": "2.1.0",
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "runs": [{
    "tool": { "driver": { "name": "CyberStrikeAI", "version": "1.7.17" } },
    "results": [{
      "ruleId": "owasp/a1-injection-sqli",
      "level": "error",
      "message": { "text": "SQL Injection at /search?q=" },
      "locations": [{ "physicalLocation": { "artifactLocation": { "uri": "https://target/search" } } }],
      "partialFingerprints": { "reproduction": "curl 'https://target/search?q=1%27'" }
    }]
  }]
}
```

- `level` 映射：Critical/High → `error`；Medium → `warning`；Low → `note`
- `ruleId` 用 OWASP/VRT slug，便于跨工具去重

## FP Triage LLM 提示（判定真假阳性）

每次记录发现前，用此提示让 LLM 复核：

```
你是漏洞 triage 专家。判断以下发现是真阳性还是假阳性，仅基于提供的证据。

发现：
- 漏洞类：{class}
- 目标：{target}
- 指标：{indicator}
- 复现：{reproduction}

返回 JSON（仅一行）：
{"confidence": 0.0~1.0, "false_positive": true|false, "reason": "基于指标的具体判断依据"}

规则：
- 指标必须是具体证据（状态码/响应体/OOB 命中），不是「页面看起来像」。
- 重复请求无新信息不算验证。
- confidence >= 0.8 且 false_positive=false 才标 supported；否则标 待验证。
```

## 与 CyberStrike 的衔接

- `record_vulnerability` 写入时附带 `reproduction` 字段，供 triage 与后续 confirmation 复用。
- confirmation：重跑 Reproduction 命令，不命中则降权（pheromone 衰减）。
- 报告生成时按会话所选「报告目标平台」选模板。
