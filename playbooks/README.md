# CyberStrikeAI Playbooks

分阶段攻击链编排，源自 Pentest-Swarm-AI 的 playbook 概念。每个 playbook 是一个 YAML，结构：

```yaml
phases:
  - name: 侦察
    tools:
      - { name: subfinder, options: ... }
      - { name: httpx, options: ... }
    post_analysis: |
      LLM 阶段间分析提示：从侦察结果中筛选存活高价值目标，交给下一阶段...
  - name: 漏洞发现
    tools: [...]
    post_analysis: |
      ...
```

## 现有 playbooks

| 文件 | 场景 |
|------|------|
| `owasp-top10.yaml` | OWASP Top 10 全量覆盖 |
| `api-security.yaml` | API 专项（BOLA/批量分配/GraphQL） |
| `bug-bounty.yaml` | 赏金猎人视角（高赔付漏洞类先验） |
| `ctf-solver.yaml` | CTF 解题（web/pwn/crypto/reverse/forensics） |
| `external-asm.yaml` | 外部攻击面管理（子域/端口/服务发现） |
| `internal-network.yaml` | 内网渗透（横向/提权/AD） |
| `ci-cd-security.yaml` | CI/CD 供应链安全 |
| `pheromones.yaml` | 信息素调参：finding 类型权重 + 衰减半衰期 |

## 与 CyberStrike 的衔接

- CyberStrike 的 `roles/*.yaml` 是单层 `user_prompt`；playbook 在其之上提供**分阶段工具编排 + 阶段间 LLM 分析**。
- `pheromones.yaml` 的权重/衰减可映射到平台黑板的 fact 权重：陈旧发现自动降权。
- playbook 里的 `tools[].name` 对应 `tools/*.yaml` 的工具名（含本仓库新增的 `httpx`/`naabu`/`dnsx`/`interactsh`/`jwt_tool`/`ysoserial`/`gitleaks` 等）。

> 仅用于已授权安全测试。每个 playbook 的 phases 都假设已有授权范围。
