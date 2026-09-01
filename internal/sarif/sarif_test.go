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
