package evals

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRouteScore 词面得分的正/负/边界 case。
func TestRouteScore(t *testing.T) {
	cases := []struct {
		query, desc string
		wantMin     float64
		wantMax     float64
	}{
		{"nmap 端口扫描", "对目标执行 nmap 端口扫描与服务识别", 0.9, 1.01}, // 全命中
		{"nmap 端口扫描", "web 漏洞扫描工具", 0, 0.41},             // 低命中（"扫描"字面撞上 0.4）
		{"", "任何描述", 0, 0.01},                            // 空查询
		{"sql 注入检测", "sqli sql 注入注入点检测", 0.5, 1.01},      // 部分命中
	}
	for _, c := range cases {
		got := routeScore(c.query, c.desc)
		if got < c.wantMin || got >= c.wantMax {
			t.Errorf("routeScore(%q,%q)=%v, want [%v,%v)", c.query, c.desc, got, c.wantMin, c.wantMax)
		}
	}
}

// TestExtractDescription frontmatter description 提取（单行/折叠/缺失）。
func TestExtractDescription(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"单行", "---\nname: a\ndescription: 端口扫描工具\n---\nbody", "端口扫描工具"},
		{"折叠块", "---\nname: a\ndescription: >-\n  第一行\n  第二行\n---\nbody", "第一行 第二行"},
		{"缺失", "---\nname: a\n---\nbody", ""},
		{"无 frontmatter", "body only", ""},
	}
	for _, c := range cases {
		got := extractDescription(c.content)
		if got != c.want {
			t.Errorf("%s: extractDescription=%q, want %q", c.name, got, c.want)
		}
	}
}

// TestEvaluateRouteBehavior 端到端：临时 skills 目录 + 用例集，验证通过/偏差判定。
func TestEvaluateRouteBehavior(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, desc string) {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文"
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("nmap-scan", "对目标执行 nmap 端口扫描与服务版本识别，支持全端口与拓扑发现")
	mk("web-vuln", "web 应用漏洞扫描，xss sql 注入检测与爬虫")
	mk("subdomain-enum", "子域名枚举与 dns 收集，爆破子域")

	cases := []RouteCase{
		{Query: "用 nmap 扫描目标端口", ExpectedSkill: "nmap-scan"},
		{Query: "检测 web 漏洞 xss", ExpectedSkill: "web-vuln"},
		{Query: "枚举子域名", ExpectedSkill: "subdomain-enum"},
	}
	res, err := EvaluateRouteBehavior(dir, cases)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed != 3 || len(res.Misses) != 0 {
		t.Errorf("expected 3/3 pass, got %d pass, misses=%+v", res.Passed, res.Misses)
	}

	// 偏差 case：目标 description 与查询完全脱节
	mk("bad-skill", "完全无关的描述文本内容")
	res2, err := EvaluateRouteBehavior(dir, []RouteCase{
		{Query: "用 nmap 扫描目标端口", ExpectedSkill: "bad-skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Passed != 0 || len(res2.Misses) != 1 {
		t.Errorf("expected 0 pass with 1 miss, got %d pass, misses=%+v", res2.Passed, res2.Misses)
	}
	if res2.Misses[0].Reason == "" {
		t.Errorf("miss 应带 reason")
	}
}

// TestFormatRouteReport 报告格式化。
func TestFormatRouteReport(t *testing.T) {
	r := &RouteBehaviorResult{
		TotalCases: 2, Passed: 1,
		Misses: []RouteMiss{{Query: "q", Expected: "s", Reason: "词面证据不足"}},
	}
	out := FormatRouteReport(r)
	if out == "" || !contains(out, "1/2") || !contains(out, "词面证据不足") {
		t.Errorf("FormatRouteReport 输出不符: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
