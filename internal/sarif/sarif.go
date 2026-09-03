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
	// LogicalLocations DAST 端点锚定：把无源码的动态扫描结果锚定到逻辑端点
	// （如 "GET /api/v1/users/{id}"）。SARIF 2.1.0 run.logicalLocations。
	LogicalLocations []LogicalLocation `json:"logicalLocations,omitempty"`
	// VersionControlProvenance 版本控制溯源：绑定扫描时的 commit/branch/tag，
	// 便于平台侧定位"哪一次代码状态扫出的"。SARIF 2.1.0 run.versionControlProvenance。
	VersionControlProvenance []VersionControlDetails `json:"versionControlProvenance,omitempty"`
	// AutomationDetails 自动化执行信息（可选，记录本次扫描的 run id / 起止时间）。
	AutomationDetails *AutomationDetails `json:"automationDetails,omitempty"`
	// Invocations 执行调用记录（可选）。
	Invocations []Invocation `json:"invocations,omitempty"`
}

// Tool 执行扫描的工具描述。
type Tool struct {
	Driver Driver `json:"driver"`
}

// Driver 工具驱动元信息，含 rules 定义（去重后的规则集）。
type Driver struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// InformationURI 工具信息链接（SARIF 2.1.0 driver.informationUri）。
	InformationURI string `json:"informationUri,omitempty"`
	Rules          []Rule `json:"rules"`
}

// Rule 规则定义。一个 CWE 对应一条 rule。
type Rule struct {
	ID               string `json:"id"`
	ShortDescription Text   `json:"shortDescription"`
	// FullDescription 完整描述（可选）。
	FullDescription *Text `json:"fullDescription,omitempty"`
	// HelpURI 规则帮助链接（如 CWE 官方页）。
	HelpURI string `json:"helpUri,omitempty"`
	// DefaultConfiguration 规则默认级别配置。
	DefaultConfiguration *RuleConfiguration `json:"defaultConfiguration,omitempty"`
}

// RuleConfiguration 规则默认配置：level + security-severity。
type RuleConfiguration struct {
	Level string `json:"level"`
	// SecuritySeverity 0-10 字符串（critical=9-10/high=7-8.9/medium=4-6.9/low=0-3.9），
	// 供 GitHub Security tab 排序与分级展示。序列化为 properties.security-severity。
	Properties SeverityProperties `json:"properties,omitempty"`
}

// SeverityProperties 承载 security-severity 字段。
type SeverityProperties struct {
	// SecuritySeverity 0-10 字符串供 GitHub 排序。
	SecuritySeverity string `json:"security-severity,omitempty"`
}

// Result 单条扫描结果（一条漏洞）。
type Result struct {
	RuleID              string            `json:"ruleId"`
	Level               string            `json:"level"`
	Message             Text              `json:"message"`
	Locations           []Location        `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
	// CodeFlows 代码流/数据流（DAST 可填请求-响应链路）。
	CodeFlows []CodeFlow `json:"codeFlows,omitempty"`
	// Fixes 一键修复建议（fix_before/after 片段）。
	Fixes []Fix `json:"fixes,omitempty"`
	// Properties 扩展属性，承载 security-severity / verifier evidence_level 等。
	Properties *ResultProperties `json:"properties,omitempty"`
}

// ResultProperties 结果扩展属性。security-severity 为 0-10 字符串供 GitHub 排序。
type ResultProperties struct {
	// SecuritySeverity 0-10 字符串（critical=9-10/high=7-8.9/medium=4-6.9/low=0-3.9）。
	SecuritySeverity string `json:"security-severity,omitempty"`
	// EvidenceLevel verifier 闸门裁定的证据等级（suspected/corroborated/reproduced/impact_proven）。
	EvidenceLevel string `json:"evidence-level,omitempty"`
	// Confirmed verifier 是否过闸（true/false 字符串）。
	Confirmed string `json:"confirmed,omitempty"`
	// Category 原始漏洞 category（中文/英文）。
	Category string `json:"category,omitempty"`
	// Target 精确目标（URL/IP）。
	Target string `json:"target,omitempty"`
	// Reproduction 复现指令（curl/HTTP 请求）。
	Reproduction string `json:"reproduction,omitempty"`
}

// Location 结果位置。
type Location struct {
	PhysicalLocation PhysicalLocation `json:"physicalLocation"`
	// LogicalLocation 逻辑位置索引（指向 run.logicalLocations），DAST 端点锚定用。
	LogicalLocation *LogicalLocation `json:"logicalLocation,omitempty"`
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

// SyntheticLocation 兜底合成位置：当 DAST 找不到真实文件锚点时，
// 用一个稳定的合成 URI（如 "synthetic:<category>:<target>"）保证 SARIF 合规。
// 这避免了 result.locations 为空导致 GitHub Code Scanning 拒绝导入。
func SyntheticLocation(category, target string) string {
	return fmt.Sprintf("synthetic:%s:%s", category, target)
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

// LogicalLocation 逻辑位置：DAST 端点锚定（如 "GET /api/v1/users/{id}"）。
// SARIF 2.1.0 logicalLocation 对象。
type LogicalLocation struct {
	// Name 逻辑位置名称（如 "GET /api/users"）。
	Name string `json:"name,omitempty"`
	// FullyQualifiedName 完全限定名（含基址，如 "GET http://target/api/users"）。
	FullyQualifiedName string `json:"fullyQualifiedName,omitempty"`
	// Kind 逻辑位置种类（"url"/"endpoint"/"resource"等）。
	Kind string `json:"kind,omitempty"`
	// ParentIndex 父逻辑位置索引（用于构建端点层级）。
	ParentIndex int `json:"parentIndex,omitempty"`
}

// VersionControlDetails 版本控制溯源细节。
type VersionControlDetails struct {
	// RepositoryURI 仓库 URI（如 GitHub repo URL）。
	RepositoryURI string `json:"repositoryUri,omitempty"`
	// RevisionID commit SHA / tag。
	RevisionID string `json:"revisionId,omitempty"`
	// Branch 分支名。
	Branch string `json:"branch,omitempty"`
	// Tag 标签名。
	Tag string `json:"tag,omitempty"`
	// AsOfTimeISO8601 扫描对应的时间点（ISO8601）。
	AsOfTimeISO8601 string `json:"asOfTimeIso8601,omitempty"`
	// MappedTo 映射到的代码位置（可选）。
	MappedTo *ArtifactLocation `json:"mappedTo,omitempty"`
}

// AutomationDetails 自动化执行细节。
type AutomationDetails struct {
	// ID 本次扫描运行的标识（如 "cyberstrike-2026-09-04-run-001"）。
	ID string `json:"id,omitempty"`
	// CommandLine 扫描命令行（可选）。
	CommandLine string `json:"commandLine,omitempty"`
	// StartTimeISO8601 起始时间。
	StartTimeISO8601 string `json:"startTimeIso8601,omitempty"`
	// EndTimeISO8601 结束时间。
	EndTimeISO8601 string `json:"endTimeIso8601,omitempty"`
}

// Invocation 单次工具执行调用记录。
type Invocation struct {
	ExecutionSuccessful bool   `json:"executionSuccessful"`
	StartTimeISO8601    string `json:"startTimeIso8601,omitempty"`
	EndTimeISO8601      string `json:"endTimeIso8601,omitempty"`
	CommandLine         string `json:"commandLine,omitempty"`
	ExitCode            int    `json:"exitCode,omitempty"`
	ExitCodeDescription string `json:"exitCodeDescription,omitempty"`
}

// CodeFlow 代码流/数据流：DAST 可填请求-响应链路以增强可追溯性。
type CodeFlow struct {
	Message     Text         `json:"message,omitempty"`
	ThreadFlows []ThreadFlow `json:"threadFlows"`
}

// ThreadFlow 单线程流。
type ThreadFlow struct {
	Locations []ThreadFlowLocation `json:"locations"`
}

// ThreadFlowLocation 流位置。
type ThreadFlowLocation struct {
	Location       Location `json:"location"`
	Importance     string   `json:"importance,omitempty"`
	ExecutionOrder int      `json:"executionOrder,omitempty"`
}

// Fix 一键修复建议：包含前后片段，平台侧可渲染 diff。
type Fix struct {
	// Description 修复说明。
	Description Text `json:"description,omitempty"`
	// ArtifactChanges 制品变更（fix_before/after 片段）。
	ArtifactChanges []ArtifactChange `json:"artifactChanges"`
}

// ArtifactChange 单个制品的修复变更。
type ArtifactChange struct {
	// ArtifactLocation 被修改的制品（URL/文件路径/synthetic URI）。
	ArtifactLocation ArtifactLocation `json:"artifactLocation"`
	// Replacements 替换片段列表（before → after）。
	Replacements []Replacement `json:"replacements"`
}

// Replacement 单个替换片段：before/after，平台侧可一键应用。
type Replacement struct {
	// DeletedRegion 被删除的区域。
	DeletedRegion Region `json:"deletedRegion"`
	// InsertedContent 插入的内容。
	InsertedContent Text `json:"insertedContent"`
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
	// Reproduction 复现指令（curl/HTTP 请求），可选；填入 result.properties.reproduction。
	Reproduction string
	// EvidenceLevel verifier 闸门裁定的证据等级，可选；填入 result.properties.evidence-level。
	EvidenceLevel string
	// Confirmed verifier 是否过闸（"true"/"false"），可选；填入 result.properties.confirmed。
	Confirmed string
}

// ReportOptions 生成报告的可选项：版本控制溯源、DAST 端点锚定、自动化细节。
// 全部为零值时退化为最小 SARIF（向后兼容老调用方）。
type ReportOptions struct {
	// RepositoryURI 版本控制仓库 URI。
	RepositoryURI string
	// RevisionID commit SHA / tag。
	RevisionID string
	// Branch 分支名。
	Branch string
	// Tag 标签名。
	Tag string
	// AsOfTimeISO8601 扫描时间点（ISO8601）。
	AsOfTimeISO8601 string
	// AutomationID 本次扫描 run id。
	AutomationID string
	// InformationURI 工具信息链接。
	InformationURI string
	// CWEBaseURI CWE 详情基址（默认 https://cwe.mitre.org/data/definitions/）。
	CWEBaseURI string
}

// defaultCWEBaseURI 默认 CWE 详情基址。
const defaultCWEBaseURI = "https://cwe.mitre.org/data/definitions/"

// cweHelpURI 构造 CWE 详情页 URL（如 https://cwe.mitre.org/data/definitions/79.html）。
// ruleID 形如 "CWE-79"，输出 ".../79.html"；无法解析时返回空。
func cweHelpURI(ruleID, baseURI string) string {
	if baseURI == "" {
		baseURI = defaultCWEBaseURI
	}
	// ruleID 形如 "CWE-79"
	if !strings.HasPrefix(strings.ToUpper(ruleID), "CWE-") {
		return ""
	}
	num := strings.TrimPrefix(strings.ToUpper(ruleID), "CWE-")
	if num == "" {
		return ""
	}
	return baseURI + num + ".html"
}

// severityToSecurityScore 把平台 severity 映射为 0-10 的 security-severity 字符串。
// critical: 9-10 / high: 7-8.9 / medium: 4-6.9 / low: 0-3.9 / info: 0。
// 取区间中位值，便于 GitHub Security tab 排序。
func severityToSecurityScore(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return "9.5"
	case "high":
		return "7.5"
	case "medium":
		return "5.0"
	case "low":
		return "2.0"
	case "info", "":
		return "0.0"
	default:
		return "0.0"
	}
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
//
// 向后兼容：本函数不填 versionControlProvenance / logicalLocations / properties
// 等生产级字段，仅输出最小可用 SARIF。需要生产级字段请用 FromVulnerabilitiesWithOptions。
func FromVulnerabilities(vulns []VulnerabilityInput) Report {
	return FromVulnerabilitiesWithOptions(vulns, ReportOptions{})
}

// FromVulnerabilitiesWithOptions 生成生产级 SARIF 报告：补齐 logicalLocations
// （DAST 端点锚定）+ versionControlProvenance（commit/branch 绑定）+ security-severity
// （0-10 字符串供 GitHub 排序）+ fix_before/after（一键修复）+ synthetic_location 兜底。
//
// 立场保护：本函数仅做格式化输出，不对授权/真实性做判定——verifier 闸门
// 由 internal/security 包负责，本函数信任调用方传入的 EvidenceLevel/Confirmed。
func FromVulnerabilitiesWithOptions(vulns []VulnerabilityInput, opts ReportOptions) Report {
	ruleSet := make(map[string]struct{}, 0)
	rules := make([]Rule, 0)
	results := make([]Result, 0, len(vulns))
	// logicalLocations 索引：按 "METHOD base path" 去重，result 引用其 index。
	logicalIndex := make(map[string]int, 0)
	logicalLocations := make([]LogicalLocation, 0)

	cweBase := opts.CWEBaseURI
	if cweBase == "" {
		cweBase = defaultCWEBaseURI
	}

	for _, v := range vulns {
		ruleID := categoryToCWE(v.Type)
		if _, ok := ruleSet[ruleID]; !ok {
			ruleSet[ruleID] = struct{}{}
			rule := Rule{
				ID:               ruleID,
				ShortDescription: Text{Text: v.Title},
				HelpURI:          cweHelpURI(ruleID, cweBase),
				DefaultConfiguration: &RuleConfiguration{
					Level: severityToLevel(v.Severity),
					Properties: SeverityProperties{
						SecuritySeverity: severityToSecurityScore(v.Severity),
					},
				},
			}
			rules = append(rules, rule)
		}

		msg := v.Title
		if v.Description != "" {
			msg = v.Title + ": " + v.Description
		}
		if msg == "" {
			msg = ruleID
		}

		// 物理位置：优先用 Target；Target 为空时用 synthetic 兜底，保证 SARIF 合规。
		uri := v.Target
		if strings.TrimSpace(uri) == "" {
			uri = SyntheticLocation(v.Type, v.Target)
		}
		loc := Location{
			PhysicalLocation: PhysicalLocation{
				ArtifactLocation: ArtifactLocation{
					URI: uri,
				},
			},
		}
		if v.LineNumber > 0 {
			loc.PhysicalLocation.Region = &Region{StartLine: v.LineNumber}
		}

		// 逻辑位置（DAST 端点锚定）：从 Target 解析 method + path 作为锚点。
		// 同一锚点去重，result 引用 index。
		logicalKey := parseLogicalKey(v.Target)
		if logicalKey != "" {
			if idx, ok := logicalIndex[logicalKey]; ok {
				loc.LogicalLocation = &LogicalLocation{
					Name:        logicalKey,
					Kind:        "url",
					ParentIndex: idx,
				}
			} else {
				idx := len(logicalLocations)
				logicalIndex[logicalKey] = idx
				logicalLocations = append(logicalLocations, LogicalLocation{
					Name:               logicalKey,
					FullyQualifiedName: logicalKey,
					Kind:               "url",
				})
				loc.LogicalLocation = &LogicalLocation{
					Name:        logicalKey,
					Kind:        "url",
					ParentIndex: idx,
				}
			}
		}

		// 扩展属性：security-severity + evidence-level + confirmed + category + target + reproduction
		props := &ResultProperties{
			SecuritySeverity: severityToSecurityScore(v.Severity),
			Category:         v.Type,
			Target:           v.Target,
			Reproduction:     v.Reproduction,
		}
		if v.EvidenceLevel != "" {
			props.EvidenceLevel = v.EvidenceLevel
		}
		if v.Confirmed != "" {
			props.Confirmed = v.Confirmed
		}

		results = append(results, Result{
			RuleID:              ruleID,
			Level:               severityToLevel(v.Severity),
			Message:             Text{Text: msg},
			Locations:           []Location{loc},
			PartialFingerprints: fingerprint(ruleID, v.Target, v.Title),
			Properties:          props,
		})
	}

	run := Run{
		Tool: Tool{
			Driver: Driver{
				Name:    ToolName,
				Version: ToolVersion,
				Rules:   rules,
			},
		},
		Results: results,
	}

	// 版本控制溯源
	if opts.RepositoryURI != "" || opts.RevisionID != "" || opts.Branch != "" || opts.Tag != "" || opts.AsOfTimeISO8601 != "" {
		run.VersionControlProvenance = []VersionControlDetails{{
			RepositoryURI:   opts.RepositoryURI,
			RevisionID:      opts.RevisionID,
			Branch:          opts.Branch,
			Tag:             opts.Tag,
			AsOfTimeISO8601: opts.AsOfTimeISO8601,
		}}
	}

	// DAST 端点锚定
	if len(logicalLocations) > 0 {
		run.LogicalLocations = logicalLocations
	}

	// 工具信息链接
	if opts.InformationURI != "" {
		run.Tool.Driver.InformationURI = opts.InformationURI
	}

	// 自动化执行细节
	if opts.AutomationID != "" {
		run.AutomationDetails = &AutomationDetails{
			ID: opts.AutomationID,
		}
	}

	return Report{
		Version: Version,
		Schema:  Schema,
		Runs:    []Run{run},
	}
}

// parseLogicalKey 从 target（URL 或 host:port）解析出逻辑端点键。
// 形如 "http://target/api/users?id=1" → "GET http://target/api/users"。
// 无法解析时返回空串（result 不挂 LogicalLocation）。
// 注意：DAST 不一定知道 HTTP method，默认填 "GET"；调用方可后续覆盖。
func parseLogicalKey(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	// 剥离 query string
	if i := strings.Index(t, "?"); i >= 0 {
		t = t[:i]
	}
	// 剥离 fragment
	if i := strings.Index(t, "#"); i >= 0 {
		t = t[:i]
	}
	// 必须含 scheme 或 host:path 形态才算端点
	if !strings.Contains(t, "://") && !strings.Contains(t, "/") {
		return ""
	}
	return "GET " + t
}

// WriteReport 把报告以 JSON 编码写入 w。
func WriteReport(report Report, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
