package sarif

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestCategoryToCWE 覆盖各类 category → CWE 映射 + 缺省兜底。
func TestCategoryToCWE(t *testing.T) {
	cases := []struct {
		name     string
		category string
		want     string
	}{
		{"XSS 中文", "跨站脚本(XSS)", "CWE-79"},
		{"XSS 英文", "XSS", "CWE-79"},
		{"SQL注入中文", "SQL注入", "CWE-89"},
		{"SQL injection 英文", "SQL injection", "CWE-89"},
		{"路径遍历", "路径遍历", "CWE-22"},
		{"path traversal", "path traversal", "CWE-22"},
		{"命令注入", "命令注入", "CWE-77"},
		{"RCE", "RCE", "CWE-77"},
		{"代码执行", "远程代码执行", "CWE-77"},
		{"SSRF", "SSRF", "CWE-918"},
		{"CSRF", "CSRF", "CWE-352"},
		{"文件上传", "文件上传", "CWE-434"},
		{"XXE", "XXE", "CWE-611"},
		{"反序列化", "反序列化", "CWE-502"},
		{"LFI", "LFI", "CWE-98"},
		{"IDOR", "IDOR", "CWE-639"},
		{"越权", "越权访问", "CWE-639"},
		{"开放重定向", "开放重定向", "CWE-601"},
		{"信息泄露", "敏感信息泄露", "CWE-200"},
		{"弱口令", "弱口令", "CWE-521"},
		{"未授权", "未授权访问", "CWE-306"},
		{"通用注入", "注入", "CWE-74"},
		{"空 category 兜底", "", "CWE-22"},
		{"未知 category 兜底", "某个奇怪漏洞", "CWE-22"},
		{"已带 CWE 前缀", "CWE-79", "CWE-79"},
		{"已带 CWE 前缀小写", "cwe-89", "CWE-89"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := categoryToCWE(tc.category)
			if got != tc.want {
				t.Errorf("categoryToCWE(%q) = %q, want %q", tc.category, got, tc.want)
			}
		})
	}
}

// TestSeverityToLevel 覆盖 severity 压缩。
func TestSeverityToLevel(t *testing.T) {
	cases := []struct {
		severity string
		want     string
	}{
		{"critical", "error"},
		{"high", "error"},
		{"medium", "warning"},
		{"low", "note"},
		{"info", "note"},
		{"", "note"},
		{"CRITICAL", "error"}, // 大小写不敏感
		{"unknown", "note"},   // 未知兜底 note
	}
	for _, tc := range cases {
		got := severityToLevel(tc.severity)
		if got != tc.want {
			t.Errorf("severityToLevel(%q) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}

// TestFromVulnerabilities 验证整体转换：CWE 映射 + severity 压缩 + RuleID 格式 + rules 去重 + 指纹。
func TestFromVulnerabilities(t *testing.T) {
	vulns := []VulnerabilityInput{
		{
			ID: "v1", Title: "登录页SQL注入", Description: "username 参数可注入",
			Severity: "critical", Type: "SQL注入", Target: "http://target/login.php",
		},
		{
			ID: "v2", Title: "搜索框XSS", Description: "q 参数反射型 XSS",
			Severity: "medium", Type: "XSS", Target: "http://target/search",
		},
		{
			ID: "v3", Title: "另一处SQL注入", Description: "id 参数可注入",
			Severity: "high", Type: "SQL注入", Target: "http://target/item",
		},
		{
			ID: "v4", Title: "低危信息泄露", Description: "版本号暴露",
			Severity: "low", Type: "信息泄露", Target: "http://target/",
		},
	}

	report := FromVulnerabilities(vulns)

	// 顶层字段
	if report.Version != "2.1.0" {
		t.Errorf("Version = %q, want 2.1.0", report.Version)
	}
	if !strings.HasPrefix(report.Schema, "https://") || !strings.Contains(report.Schema, "sarif-schema-2.1.0.json") {
		t.Errorf("Schema 格式异常: %q", report.Schema)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("Runs 长度 = %d, want 1", len(report.Runs))
	}

	run := report.Runs[0]
	// 工具元信息
	if run.Tool.Driver.Name != "CyberStrikeAI" {
		t.Errorf("Driver.Name = %q", run.Tool.Driver.Name)
	}
	// rules 去重：SQL注入 + XSS + 信息泄露 = 3 条 rule
	if len(run.Tool.Driver.Rules) != 3 {
		t.Errorf("Rules 长度 = %d, want 3（去重后）", len(run.Tool.Driver.Rules))
	}
	// RuleID 格式校验
	for _, r := range run.Tool.Driver.Rules {
		if !strings.HasPrefix(r.ID, "CWE-") {
			t.Errorf("Rule.ID 格式异常: %q", r.ID)
		}
	}

	// 4 条 result
	if len(run.Results) != 4 {
		t.Fatalf("Results 长度 = %d, want 4", len(run.Results))
	}

	// 逐条校验 RuleID + Level
	r0 := run.Results[0]
	if r0.RuleID != "CWE-89" {
		t.Errorf("result[0].RuleID = %q, want CWE-89", r0.RuleID)
	}
	if r0.Level != "error" { // critical → error
		t.Errorf("result[0].Level = %q, want error", r0.Level)
	}
	if r0.Locations[0].PhysicalLocation.ArtifactLocation.URI != "http://target/login.php" {
		t.Errorf("result[0].URI 异常: %q", r0.Locations[0].PhysicalLocation.ArtifactLocation.URI)
	}

	r1 := run.Results[1]
	if r1.RuleID != "CWE-79" {
		t.Errorf("result[1].RuleID = %q, want CWE-79", r1.RuleID)
	}
	if r1.Level != "warning" { // medium → warning
		t.Errorf("result[1].Level = %q, want warning", r1.Level)
	}

	r2 := run.Results[2]
	if r2.RuleID != "CWE-89" {
		t.Errorf("result[2].RuleID = %q, want CWE-89", r2.RuleID)
	}
	if r2.Level != "error" { // high → error
		t.Errorf("result[2].Level = %q, want error", r2.Level)
	}

	r3 := run.Results[3]
	if r3.RuleID != "CWE-200" {
		t.Errorf("result[3].RuleID = %q, want CWE-200", r3.RuleID)
	}
	if r3.Level != "note" { // low → note
		t.Errorf("result[3].Level = %q, want note", r3.Level)
	}

	// 指纹去重字段存在
	if r0.PartialFingerprints["primary"] == "" {
		t.Error("PartialFingerprints.primary 为空")
	}
	if !strings.Contains(r0.PartialFingerprints["primary"], "CWE-89") {
		t.Errorf("指纹未含 ruleId: %q", r0.PartialFingerprints["primary"])
	}
}

// TestFromVulnerabilitiesEmpty 空输入应返回合法的空 SARIF（1 个 run，0 result）。
func TestFromVulnerabilitiesEmpty(t *testing.T) {
	report := FromVulnerabilities(nil)
	if report.Version != "2.1.0" {
		t.Errorf("Version = %q", report.Version)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("Runs 长度 = %d, want 1", len(report.Runs))
	}
	if len(report.Runs[0].Results) != 0 {
		t.Errorf("空输入 Results 长度 = %d, want 0", len(report.Runs[0].Results))
	}
	if len(report.Runs[0].Tool.Driver.Rules) != 0 {
		t.Errorf("空输入 Rules 长度 = %d, want 0", len(report.Runs[0].Tool.Driver.Rules))
	}
}

// TestWriteReport 验证输出是合法 JSON 且字段名符合 SARIF 规范（camelCase）。
func TestWriteReport(t *testing.T) {
	vulns := []VulnerabilityInput{
		{ID: "v1", Title: "XSS", Severity: "high", Type: "XSS", Target: "http://t"},
	}
	report := FromVulnerabilities(vulns)

	var buf bytes.Buffer
	if err := WriteReport(report, &buf); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	// 应是合法 JSON
	var raw map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	// 顶层字段
	if raw["version"] != "2.1.0" {
		t.Errorf("JSON version = %v", raw["version"])
	}
	if _, ok := raw["$schema"]; !ok {
		t.Error("JSON 缺少 $schema 字段")
	}
	runs, ok := raw["runs"].([]interface{})
	if !ok || len(runs) != 1 {
		t.Fatalf("JSON runs 异常: %v", raw["runs"])
	}
	run := runs[0].(map[string]interface{})
	// SARIF 规范字段名校验
	if _, ok := run["tool"]; !ok {
		t.Error("JSON run 缺少 tool 字段")
	}
	if _, ok := run["results"]; !ok {
		t.Error("JSON run 缺少 results 字段")
	}
	// result 字段名（camelCase）
	results := run["results"].([]interface{})
	r0 := results[0].(map[string]interface{})
	for _, key := range []string{"ruleId", "level", "message", "locations", "partialFingerprints"} {
		if _, ok := r0[key]; !ok {
			t.Errorf("JSON result 缺少字段 %q", key)
		}
	}
}

// TestSyntheticLocation 验证 synthetic 兜底 URI 格式。
func TestSyntheticLocation(t *testing.T) {
	got := SyntheticLocation("SSRF", "http://target/api")
	want := "synthetic:SSRF:http://target/api"
	if got != want {
		t.Errorf("SyntheticLocation = %q, want %q", got, want)
	}
}

// TestSeverityToSecurityScore 覆盖 severity → 0-10 字符串映射。
func TestSeverityToSecurityScore(t *testing.T) {
	cases := []struct {
		severity string
		want     string
	}{
		{"critical", "9.5"},
		{"high", "7.5"},
		{"medium", "5.0"},
		{"low", "2.0"},
		{"info", "0.0"},
		{"", "0.0"},
		{"CRITICAL", "9.5"}, // 大小写不敏感
		{"unknown", "0.0"},
	}
	for _, tc := range cases {
		got := severityToSecurityScore(tc.severity)
		if got != tc.want {
			t.Errorf("severityToSecurityScore(%q) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}

// TestCWEHelpURI 验证 CWE 详情页 URL 构造。
func TestCWEHelpURI(t *testing.T) {
	cases := []struct {
		ruleID  string
		baseURI string
		want    string
	}{
		{"CWE-79", "", "https://cwe.mitre.org/data/definitions/79.html"},
		{"cwe-89", "", "https://cwe.mitre.org/data/definitions/89.html"},
		{"CWE-918", "https://example.com/cwe/", "https://example.com/cwe/918.html"},
		{"NOT-CWE", "", ""},
		{"CWE-", "", ""},
	}
	for _, tc := range cases {
		got := cweHelpURI(tc.ruleID, tc.baseURI)
		if got != tc.want {
			t.Errorf("cweHelpURI(%q, %q) = %q, want %q", tc.ruleID, tc.baseURI, got, tc.want)
		}
	}
}

// TestParseLogicalKey 验证 DAST 端点锚点键解析。
func TestParseLogicalKey(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"http://target/api/users?id=1", "GET http://target/api/users"},
		{"http://target/api/users#frag", "GET http://target/api/users"},
		{"http://target/api/users", "GET http://target/api/users"},
		{"", ""},
		{"no-slash-host", ""},
	}
	for _, tc := range cases {
		got := parseLogicalKey(tc.target)
		if got != tc.want {
			t.Errorf("parseLogicalKey(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// TestFromVulnerabilitiesWithOptions 验证生产级字段：logicalLocations +
// versionControlProvenance + security-severity + properties + helpUri。
func TestFromVulnerabilitiesWithOptions(t *testing.T) {
	vulns := []VulnerabilityInput{
		{
			ID: "v1", Title: "SSRF", Severity: "critical", Type: "SSRF",
			Target:        "http://target/api?url=x",
			Reproduction:  "curl 'http://target/api?url=http://oast'",
			EvidenceLevel: "reproduced", Confirmed: "true",
		},
		{
			ID: "v2", Title: "XSS", Severity: "medium", Type: "XSS",
			Target:        "http://target/search?q=x",
			Reproduction:  "注入 <script>alert(1)</script>",
			EvidenceLevel: "suspected", Confirmed: "false",
		},
	}
	opts := ReportOptions{
		RepositoryURI:   "https://github.com/org/repo",
		RevisionID:      "abc123",
		Branch:          "main",
		AsOfTimeISO8601: "2026-09-04T00:00:00Z",
		AutomationID:    "run-001",
		InformationURI:  "https://cyberstrike.ai/docs",
	}
	report := FromVulnerabilitiesWithOptions(vulns, opts)

	if report.Version != "2.1.0" {
		t.Errorf("Version = %q", report.Version)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("Runs 长度 = %d", len(report.Runs))
	}
	run := report.Runs[0]

	// 版本控制溯源
	if len(run.VersionControlProvenance) != 1 {
		t.Fatalf("VersionControlProvenance 长度 = %d", len(run.VersionControlProvenance))
	}
	vcp := run.VersionControlProvenance[0]
	if vcp.RepositoryURI != "https://github.com/org/repo" {
		t.Errorf("RepositoryURI = %q", vcp.RepositoryURI)
	}
	if vcp.RevisionID != "abc123" {
		t.Errorf("RevisionID = %q", vcp.RevisionID)
	}
	if vcp.Branch != "main" {
		t.Errorf("Branch = %q", vcp.Branch)
	}

	// 自动化细节
	if run.AutomationDetails == nil || run.AutomationDetails.ID != "run-001" {
		t.Errorf("AutomationDetails.ID 异常: %+v", run.AutomationDetails)
	}

	// 工具信息链接
	if run.Tool.Driver.InformationURI != "https://cyberstrike.ai/docs" {
		t.Errorf("InformationURI = %q", run.Tool.Driver.InformationURI)
	}

	// logicalLocations：两个不同端点 → 2 条
	if len(run.LogicalLocations) != 2 {
		t.Errorf("LogicalLocations 长度 = %d, want 2", len(run.LogicalLocations))
	}

	// 规则配置：security-severity
	if len(run.Tool.Driver.Rules) != 2 {
		t.Fatalf("Rules 长度 = %d", len(run.Tool.Driver.Rules))
	}
	r0 := run.Tool.Driver.Rules[0]
	if r0.DefaultConfiguration == nil {
		t.Fatal("DefaultConfiguration 为空")
	}
	if r0.DefaultConfiguration.Properties.SecuritySeverity != "9.5" {
		t.Errorf("rule[0] security-severity = %q, want 9.5", r0.DefaultConfiguration.Properties.SecuritySeverity)
	}
	if r0.HelpURI == "" {
		t.Error("rule[0] HelpURI 为空")
	}

	// result properties
	if len(run.Results) != 2 {
		t.Fatalf("Results 长度 = %d", len(run.Results))
	}
	res0 := run.Results[0]
	if res0.Properties == nil {
		t.Fatal("result[0] Properties 为空")
	}
	if res0.Properties.SecuritySeverity != "9.5" {
		t.Errorf("result[0] security-severity = %q, want 9.5", res0.Properties.SecuritySeverity)
	}
	if res0.Properties.EvidenceLevel != "reproduced" {
		t.Errorf("result[0] evidence-level = %q, want reproduced", res0.Properties.EvidenceLevel)
	}
	if res0.Properties.Confirmed != "true" {
		t.Errorf("result[0] confirmed = %q, want true", res0.Properties.Confirmed)
	}
	if res0.Properties.Reproduction == "" {
		t.Error("result[0] reproduction 为空")
	}
	if res0.Properties.Category != "SSRF" {
		t.Errorf("result[0] category = %q", res0.Properties.Category)
	}
	// logicalLocation 锚定
	if res0.Locations[0].LogicalLocation == nil {
		t.Error("result[0] LogicalLocation 为空")
	}

	// result[1]：suspected / not confirmed
	res1 := run.Results[1]
	if res1.Properties.EvidenceLevel != "suspected" {
		t.Errorf("result[1] evidence-level = %q, want suspected", res1.Properties.EvidenceLevel)
	}
	if res1.Properties.Confirmed != "false" {
		t.Errorf("result[1] confirmed = %q, want false", res1.Properties.Confirmed)
	}
}

// TestFromVulnerabilitiesWithOptionsSynthetic 验证 Target 为空时 synthetic 兜底。
func TestFromVulnerabilitiesWithOptionsSynthetic(t *testing.T) {
	vulns := []VulnerabilityInput{
		{ID: "v1", Title: "XSS", Severity: "high", Type: "XSS", Target: ""},
	}
	report := FromVulnerabilitiesWithOptions(vulns, ReportOptions{})
	res := report.Runs[0].Results[0]
	uri := res.Locations[0].PhysicalLocation.ArtifactLocation.URI
	if !strings.HasPrefix(uri, "synthetic:") {
		t.Errorf("Target 为空应用 synthetic 兜底，got URI = %q", uri)
	}
}

// TestFromVulnerabilitiesWithOptionsJSON 验证生产级字段的 JSON 序列化字段名。
func TestFromVulnerabilitiesWithOptionsJSON(t *testing.T) {
	vulns := []VulnerabilityInput{
		{ID: "v1", Title: "XSS", Severity: "high", Type: "XSS", Target: "http://t/path"},
	}
	opts := ReportOptions{RevisionID: "deadbeef", Branch: "dev"}
	report := FromVulnerabilitiesWithOptions(vulns, opts)

	var buf bytes.Buffer
	if err := WriteReport(report, &buf); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	runs := raw["runs"].([]interface{})
	run := runs[0].(map[string]interface{})
	// 生产级字段 camelCase 校验
	if _, ok := run["logicalLocations"]; !ok {
		t.Error("JSON 缺少 logicalLocations 字段")
	}
	if _, ok := run["versionControlProvenance"]; !ok {
		t.Error("JSON 缺少 versionControlProvenance 字段")
	}
	vcp := run["versionControlProvenance"].([]interface{})[0].(map[string]interface{})
	if vcp["revisionId"] != "deadbeef" {
		t.Errorf("revisionId = %v", vcp["revisionId"])
	}
	if vcp["branch"] != "dev" {
		t.Errorf("branch = %v", vcp["branch"])
	}
	// result properties.security-severity
	results := run["results"].([]interface{})
	r0 := results[0].(map[string]interface{})
	props := r0["properties"].(map[string]interface{})
	if props["security-severity"] != "7.5" {
		t.Errorf("security-severity = %v, want 7.5", props["security-severity"])
	}
	// rule defaultConfiguration.properties.security-severity
	driver := run["tool"].(map[string]interface{})["driver"].(map[string]interface{})
	rules := driver["rules"].([]interface{})
	rule0 := rules[0].(map[string]interface{})
	dc := rule0["defaultConfiguration"].(map[string]interface{})
	dcProps := dc["properties"].(map[string]interface{})
	if dcProps["security-severity"] != "7.5" {
		t.Errorf("rule security-severity = %v, want 7.5", dcProps["security-severity"])
	}
}
