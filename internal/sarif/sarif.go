// Package sarif 实现平台漏洞到 SARIF 2.1.0 报告的转换。
//
// SARIF (Static Analysis Results Interchange Format) 是 OASIS 标准化的
// 静态分析结果交换格式，GitHub Code Scanning / Azure DevOps 等原生支持。
// 将平台漏洞导出为 SARIF 后，可直接导入这些平台的 Security tab 做聚合展示。
//
// 设计思想移植自 strix 的 sarif.py：CWE 归一化映射 + severity 压缩 +
// 指纹去重（partialFingerprints）。
package sarif

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// 报告与 Run 的常量
const (
	Version = "2.1.0"
	Schema  = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"
	// ToolName 生成报告的工具名。
	ToolName = "CyberStrikeAI"
	// ToolVersion 工具版本（占位，前端可覆盖）。
	ToolVersion = "1.0.0"
)

// Report SARIF 顶层报告。
type Report struct {
	Version string `json:"version"`
	Schema  string `json:"$schema"`
	Runs    []Run  `json:"runs"`
}

// Run SARIF run：一个工具的一次扫描结果集合。
type Run struct {
	Tool    Tool     `json:"tool"`
	Results []Result `json:"results"`
}

// Tool 执行扫描的工具描述。
type Tool struct {
	Driver Driver `json:"driver"`
}

// Driver 工具驱动元信息，含 rules 定义（去重后的规则集）。
type Driver struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Rule 规则定义。一个 CWE 对应一条 rule。
type Rule struct {
	ID               string `json:"id"`
	ShortDescription Text   `json:"shortDescription"`
}

// Result 单条扫描结果（一条漏洞）。
type Result struct {
	RuleID             string            `json:"ruleId"`
	Level              string            `json:"level"`
	Message            Text              `json:"message"`
	Locations          []Location       `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
}

// Location 结果位置。
type Location struct {
	PhysicalLocation PhysicalLocation `json:"physicalLocation"`
}

// PhysicalLocation 物理位置（URL/文件 + 行号）。
type PhysicalLocation struct {
	ArtifactLocation ArtifactLocation `json:"artifactLocation"`
	Region           *Region          `json:"region,omitempty"`
}

// ArtifactLocation 制品位置（URL 或文件路径）。
type ArtifactLocation struct {
	URI string `json:"uri"`
}

// Region 代码区域。
type Region struct {
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
}

// Text 文本片段（SARIF 用 {text: "..."} 而非裸字符串）。
type Text struct {
	Text string `json:"text"`
}

// VulnerabilityInput 平台漏洞输入。字段从 database.Vulnerability 映射而来，
// 保持解耦：SARIF 包不依赖 database 包。
type VulnerabilityInput struct {
	ID          string
	Title       string
	Description string
	Severity    string // critical/high/medium/low/info
	Type        string // 漏洞 category，如 "SQL注入"/"XSS"/"路径遍历"
	Target      string // URL 或 IP
	// LineNumber 可选行号（平台漏洞一般无行号）。
	LineNumber int
}

// severityToLevel 把平台 severity 压缩成 SARIF level。
// critical/high → error；medium → warning；low/info → note。
func severityToLevel(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	case "low", "info", "":
		return "note"
	default:
		return "note"
	}
}

// categoryToCWE 把漏洞 category 归一化为 CWE ID。
// 缺省 / 未知 category → CWE-22（路径遍历，作为通用兜底，与参考实现一致）。
// 输入不区分中英文、含/不含 "CWE-" 前缀均可。
func categoryToCWE(category string) string {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" {
		return "CWE-22"
	}
	// 若调用方已传 "CWE-79" 直接规范化返回
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(category)), "CWE-") {
		return strings.ToUpper(strings.TrimSpace(category))
	}
	switch {
	case strings.Contains(c, "xss") || strings.Contains(c, "跨站脚本"):
		return "CWE-79"
	case strings.Contains(c, "sql") || strings.Contains(c, "sql注入") || strings.Contains(c, "注入") && strings.Contains(c, "sql"):
		return "CWE-89"
	case strings.Contains(c, "路径遍历") || strings.Contains(c, "path traversal") || strings.Contains(c, "directory traversal") || strings.Contains(c, "目录遍历"):
		return "CWE-22"
	case strings.Contains(c, "命令注入") || strings.Contains(c, "command injection") || strings.Contains(c, "rce") || strings.Contains(c, "远程命令") || strings.Contains(c, "代码执行"):
		return "CWE-77"
	case strings.Contains(c, "ssrf"):
		return "CWE-918"
	case strings.Contains(c, "csrf") || strings.Contains(c, "跨站请求伪造"):
		return "CWE-352"
	case strings.Contains(c, "文件上传"):
		return "CWE-434"
	case strings.Contains(c, "xxe"):
		return "CWE-611"
	case strings.Contains(c, "反序列化") || strings.Contains(c, "deserialization"):
		return "CWE-502"
	case strings.Contains(c, "lfi") || strings.Contains(c, "文件包含") || strings.Contains(c, "file inclusion"):
		return "CWE-98"
	case strings.Contains(c, "idor") || strings.Contains(c, "越权"):
		return "CWE-639"
	case strings.Contains(c, "sqli") || strings.Contains(c, "sql注入"):
		return "CWE-89"
	case strings.Contains(c, "open redirect") || strings.Contains(c, "开放重定向") || strings.Contains(c, "url redirect"):
		return "CWE-601"
	case strings.Contains(c, "信息泄露") || strings.Contains(c, "information disclosure") || strings.Contains(c, "敏感信息"):
		return "CWE-200"
	case strings.Contains(c, "弱口令") || strings.Contains(c, "weak password"):
		return "CWE-521"
	case strings.Contains(c, "未授权") || strings.Contains(c, "unauthorized") || strings.Contains(c, "missing auth"):
		return "CWE-306"
	case strings.Contains(c, "注入"):
		return "CWE-74"
	default:
		// 兜底：路径遍历（与参考实现 strix 一致，避免空 ruleId）
		return "CWE-22"
	}
}

// fingerprint 为一条漏洞生成稳定指纹，用于 SARIF 去重。
// 组合 ruleId + target + title，保证同一目标同类型同标题的漏洞只算一条。
func fingerprint(ruleID, target, title string) map[string]string {
	return map[string]string{
		"primary": fmt.Sprintf("%s:%s:%s", ruleID, strings.TrimSpace(target), strings.TrimSpace(title)),
	}
}

// FromVulnerabilities 把平台漏洞列表转换为 SARIF 报告。
// rules 去重：同一 CWE 只在 Driver.Rules 出现一次。
func FromVulnerabilities(vulns []VulnerabilityInput) Report {
	ruleSet := make(map[string]struct{}, 0)
	rules := make([]Rule, 0)
	results := make([]Result, 0, len(vulns))

	for _, v := range vulns {
		ruleID := categoryToCWE(v.Type)
		if _, ok := ruleSet[ruleID]; !ok {
			ruleSet[ruleID] = struct{}{}
			rules = append(rules, Rule{
				ID:               ruleID,
				ShortDescription: Text{Text: v.Title},
			})
		}

		msg := v.Title
		if v.Description != "" {
			msg = v.Title + ": " + v.Description
		}
		if msg == "" {
			msg = ruleID
		}

		loc := Location{
			PhysicalLocation: PhysicalLocation{
				ArtifactLocation: ArtifactLocation{
					URI: v.Target,
				},
			},
		}
		if v.LineNumber > 0 {
			loc.PhysicalLocation.Region = &Region{StartLine: v.LineNumber}
		}

		results = append(results, Result{
			RuleID:             ruleID,
			Level:              severityToLevel(v.Severity),
			Message:            Text{Text: msg},
			Locations:          []Location{loc},
			PartialFingerprints: fingerprint(ruleID, v.Target, v.Title),
		})
	}

	return Report{
		Version: Version,
		Schema:  Schema,
		Runs: []Run{{
			Tool: Tool{
				Driver: Driver{
					Name:    ToolName,
					Version: ToolVersion,
					Rules:   rules,
				},
			},
			Results: results,
		}},
	}
}

// WriteReport 把报告以 JSON 编码写入 w。
func WriteReport(report Report, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
