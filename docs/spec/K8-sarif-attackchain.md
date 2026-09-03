# Spec: K8 — 安全深化：攻击链 + SARIF 2.1.0 输出（security deepening）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

把平台漏洞导出为 SARIF 2.1.0 标准格式（GitHub Code Scanning / Azure DevOps 原生支持），实现攻击链构建（attackchain builder）+ CWE 归一化映射 + severity 压缩 + 指纹去重。让安全验证结果可导入第三方安全平台做聚合展示。

## Tech Stack

- Go 1.25，纯标准库（`encoding/json`、`crypto/sha256`、`net/http`、`strings`）
- `internal/sarif/`：SARIF 2.1.0 报告生成（OASIS 标准 schema）
- `internal/attackchain/`：攻击链构建器（依赖 `internal/agent` + `internal/database` + `internal/openai`）
- `github.com/google/uuid` + `go.uber.org/zap`

## Commands

```bash
go vet ./internal/sarif/ ./internal/attackchain/
go test ./internal/sarif/ ./internal/attackchain/ -count=1
go build ./...
```

## Project Structure

```
internal/sarif/sarif.go              → SARIF 2.1.0 Report/Run/Result 结构 + CWE 归一化 + severity 压缩 + partialFingerprints 去重
internal/sarif/sarif_test.go         → 报告生成 + CWE 映射 + 去重测试
internal/attackchain/builder.go      → 攻击链构建器（LLM 辅助链构建 + DB 持久化）
internal/attackchain/promote_project.go → 项目级攻击链提升
internal/attackchain/truncate.go      → 攻击链截断（token 预算控制）
internal/attackchain/truncate_test.go  → 截断测试
```

## Code Style

```go
// 包注释 + 标准来源 + 移植来源（匹配 internal/sarif 风格）
// Package sarif 实现平台漏洞到 SARIF 2.1.0 报告的转换。
// SARIF 是 OASIS 标准化的静态分析结果交换格式，GitHub Code Scanning 原生支持。
// 设计思想移植自 strix 的 sarif.py：CWE 归一化映射 + severity 压缩 + 指纹去重。
const (
    Version = "2.1.0"
    Schema  = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"
    ToolName = "CyberStrikeAI"
)
```

## Testing Strategy

- `sarif_test.go`：报告生成（version=2.1.0 + schema 正确）；CWE 归一化映射（CWE-79 → XSS）；severity 压缩（critical/high/medium/low）；partialFingerprints 去重
- `truncate_test.go`：攻击链截断（token 预算控制，maxTokens 默认 100000）
- 回归底线：全仓 `go build ./...` EXIT=0

## Boundaries

- **Always**：SARIF version=2.1.0 + schema 固定；CWE 归一化用标准 CWE ID；指纹去重用 SHA256
- **Ask first**：改 SARIF version（2.1.0 → 3.0）；改 ToolName；改 maxTokens 默认值（100000）
- **Never**：删除 CWE 归一化映射（破坏 severity 压缩）；改 schema 为非 OASIS 标准；攻击链构建用真实付费 LLM（付费红线，Mock 验证）

## Success Criteria

1. SARIF 2.1.0 Report/Run/Result 结构齐全（version + schema + runs）✅ done
2. CWE 归一化映射（CWE-79 → XSS 等）✅ done
3. severity 压缩（critical/high/medium/low）✅ done
4. partialFingerprints 去重（SHA256）✅ done
5. 攻击链构建器（builder.go + promote_project.go + truncate.go）✅ done
6. 截断 token 预算控制（maxTokens 默认 100000）✅ done
7. 全仓 `go build ./...` EXIT=0 ✅ done

## Open Questions

- 攻击链构建器真实 LLM 链构建属付费红线，当前用 Mock / openai client 接口验证，真实语义评测待用户给预算
