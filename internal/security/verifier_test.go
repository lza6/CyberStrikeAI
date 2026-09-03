package security

import (
	"testing"
)

// TestEvidenceLevelString 覆盖四级证据等级的字符串序列化与未知值兜底。
func TestEvidenceLevelString(t *testing.T) {
	cases := []struct {
		level EvidenceLevel
		want  string
	}{
		{EvidenceSuspected, "suspected"},
		{EvidenceCorroborated, "corroborated"},
		{EvidenceReproduced, "reproduced"},
		{EvidenceImpactProven, "impact_proven"},
		{EvidenceLevel(99), "unknown"},
		{EvidenceLevel(0), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.level.String(); got != tc.want {
			t.Errorf("level %d String() = %q, want %q", tc.level, got, tc.want)
		}
	}
}

// TestParseEvidenceLevel 覆盖字符串解析 + 大小写不敏感 + 未知兜底。
func TestParseEvidenceLevel(t *testing.T) {
	cases := []struct {
		in   string
		want EvidenceLevel
	}{
		{"suspected", EvidenceSuspected},
		{"SUSPECTED", EvidenceSuspected},
		{"  suspected  ", EvidenceSuspected},
		{"corroborated", EvidenceCorroborated},
		{"reproduced", EvidenceReproduced},
		{"impact_proven", EvidenceImpactProven},
		{"", EvidenceSuspected},
		{"unknown", EvidenceSuspected},
		{"garbage", EvidenceSuspected},
	}
	for _, tc := range cases {
		if got := ParseEvidenceLevel(tc.in); got != tc.want {
			t.Errorf("ParseEvidenceLevel(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestLookupPlaybook 覆盖各类别匹配 + 包含匹配回退 + 未命中兜底。
func TestLookupPlaybook(t *testing.T) {
	cases := []struct {
		category string
		wantCat  string // 期望命中的 playbook.Category
		wantNil  bool
	}{
		{"SSRF", "ssrf", false},
		{"server-side request forgery", "ssrf", false},
		{"XSS", "xss", false},
		{"跨站脚本", "xss", false},
		{"SQL注入", "sqli", false},
		{"SQL injection", "sqli", false},
		{"命令注入", "command_injection", false},
		{"RCE", "command_injection", false},
		{"XXE", "xxe", false},
		{"反序列化", "deserialization", false},
		{"路径遍历", "path_traversal", false},
		{"LFI", "path_traversal", false},
		{"some ssrf variant", "ssrf", false}, // 包含匹配
		{"", "", true},
		{"完全未知类别", "", true},
	}
	for _, tc := range cases {
		got := lookupPlaybook(tc.category)
		if tc.wantNil {
			if got != nil {
				t.Errorf("lookupPlaybook(%q) 期望 nil，得到 %q", tc.category, got.Category)
			}
			continue
		}
		if got == nil {
			t.Errorf("lookupPlaybook(%q) 期望 %q，得到 nil", tc.category, tc.wantCat)
			continue
		}
		if got.Category != tc.wantCat {
			t.Errorf("lookupPlaybook(%q) = %q, want %q", tc.category, got.Category, tc.wantCat)
		}
	}
}

// TestVerifySSRFOASTPass SSRF 命中 OAST 回调 + 4-axis 全过 → confirmed。
func TestVerifySSRFOASTPass(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-ssrf-1",
		Category:      "SSRF",
		Target:        "http://target/api?url=",
		Indicator:     "OAST 回调命中 interactsh",
		Reproduction:  "curl 'http://target/api?url=http://oast.example'",
		EvidenceLevel: EvidenceReproduced,
		PlaybookMatch: "oast_callback",
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("SSRF+OAST 全过应 confirmed，got=%v reason=%s", res.Confirmed, res.Reason)
	}
	if res.FinalLevel != EvidenceReproduced {
		t.Errorf("FinalLevel = %s, want reproduced", res.FinalLevel)
	}
	if res.RuleOutHit != "" {
		t.Errorf("不应命中 RULE_OUT，got %q", res.RuleOutHit)
	}
}

// TestVerifySSRFTimeoutOnlyRuleOut SSRF 仅超时（无 OOB/响应差异）→ RULE_OUT 否决。
func TestVerifySSRFTimeoutOnlyRuleOut(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-ssrf-2",
		Category:      "SSRF",
		Target:        "http://target/api?url=",
		Indicator:     "请求超时无响应",
		Reproduction:  "curl 'http://target/api?url=http://internal:8080'  # timeout_only",
		EvidenceLevel: EvidenceReproduced,
		PlaybookMatch: "timeout_only",
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("SSRF+timeout_only 应被 RULE_OUT 否决，got confirmed")
	}
	if res.RuleOutHit != "timeout_only" {
		t.Errorf("RuleOutHit = %q, want timeout_only", res.RuleOutHit)
	}
	if res.FinalLevel != EvidenceSuspected {
		t.Errorf("FinalLevel = %s, want suspected", res.FinalLevel)
	}
}

// TestVerifyXSSAlertExecutedPass XSS 命中 alert_executed → confirmed。
func TestVerifyXSSAlertExecutedPass(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-xss-1",
		Category:      "XSS",
		Target:        "http://target/search?q=",
		Indicator:     "alert(1) 执行",
		Reproduction:  "注入 <script>alert(1)</script>",
		EvidenceLevel: EvidenceReproduced,
		PlaybookMatch: "alert_executed",
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("XSS+alert_executed 全过应 confirmed，reason=%s", res.Reason)
	}
}

// TestVerifyXSSEncodedOutputRuleOut XSS 输出被转义 → RULE_OUT 否决。
func TestVerifyXSSEncodedOutputRuleOut(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-xss-2",
		Category:      "跨站脚本",
		Target:        "http://target/q=",
		Indicator:     "&lt;script&gt; 被转义",
		PlaybookMatch: "encoded_output",
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("XSS+encoded_output 应被否决")
	}
	if res.RuleOutHit != "encoded_output" {
		t.Errorf("RuleOutHit = %q, want encoded_output", res.RuleOutHit)
	}
}

// TestVerifySQLiBooleanDifferencePass SQLi 布尔差异 → confirmed。
func TestVerifySQLiBooleanDifferencePass(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-sqli-1",
		Category:      "SQL注入",
		Target:        "http://target/login",
		Indicator:     "真/假条件响应可区分",
		PlaybookMatch: "boolean_difference",
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("SQLi+boolean_difference 应 confirmed，reason=%s", res.Reason)
	}
}

// TestVerifySQLiNoDifferenceRuleOut SQLi 真/假无差异 → RULE_OUT 否决。
func TestVerifySQLiNoDifferenceRuleOut(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-sqli-2",
		Category:      "SQL injection",
		Indicator:     "无差异响应",
		PlaybookMatch: "no_difference",
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("SQLi+no_difference 应被否决")
	}
	if res.RuleOutHit != "no_difference" {
		t.Errorf("RuleOutHit = %q, want no_difference", res.RuleOutHit)
	}
}

// TestVerifyAxisMissingFail 4-axis 缺维度 → 降级 suspected。
func TestVerifyAxisMissingFail(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-1",
		Category:      "XSS",
		Indicator:     "有信号",
		PlaybookMatch: "alert_executed",
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			// 缺 triggerable / impactful / general
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("4-axis 缺维度应 fail-closed")
	}
	if res.FinalLevel != EvidenceSuspected {
		t.Errorf("FinalLevel = %s, want suspected", res.FinalLevel)
	}
}

// TestVerifyAxisFalseFail 4-axis 某维度 Passed=false → 降级 suspected。
func TestVerifyAxisFalseFail(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-2",
		Category:      "XSS",
		Indicator:     "有信号",
		PlaybookMatch: "alert_executed",
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: false}, // 未过
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("impactful=false 应 fail-closed")
	}
	if res.FinalLevel != EvidenceSuspected {
		t.Errorf("FinalLevel = %s, want suspected", res.FinalLevel)
	}
}

// TestVerifyEvidenceTooLow evidence_level=suspected 即使 4-axis 全过也不 confirmed。
func TestVerifyEvidenceTooLow(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-3",
		Category:      "XSS",
		Indicator:     "有信号",
		PlaybookMatch: "alert_executed",
		EvidenceLevel: EvidenceSuspected, // 太低
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("evidence_level=suspected 不应 confirmed")
	}
	if res.FinalLevel != EvidenceSuspected {
		t.Errorf("FinalLevel = %s, want suspected", res.FinalLevel)
	}
}

// TestVerifyProveMissingDegrade 声明 reproduced 但缺 PROVE 锚点 → 降级 corroborated。
func TestVerifyProveMissingDegrade(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-4",
		Category:      "SSRF",
		Indicator:     "some indicator",
		PlaybookMatch: "", // 缺 PROVE 锚点
		EvidenceLevel: EvidenceReproduced,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if res.Confirmed {
		t.Errorf("缺 PROVE 锚点不应 confirmed，reason=%s", res.Reason)
	}
	if res.FinalLevel != EvidenceCorroborated {
		t.Errorf("FinalLevel = %s, want corroborated", res.FinalLevel)
	}
}

// TestVerifyCorroboratedPass evidence_level=corroborated + 4-axis 全过 → confirmed（不需 PROVE 锚点，因 < reproduced）。
func TestVerifyCorroboratedPass(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-5",
		Category:      "XSS",
		Indicator:     "多源信号印证",
		PlaybookMatch: "",
		EvidenceLevel: EvidenceCorroborated,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("corroborated + 4-axis 全过应 confirmed，reason=%s", res.Reason)
	}
	if res.FinalLevel != EvidenceCorroborated {
		t.Errorf("FinalLevel = %s, want corroborated", res.FinalLevel)
	}
}

// TestVerifyImpactProvenPass impact_proven + PROVE 锚点 → confirmed。
func TestVerifyImpactProvenPass(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-6",
		Category:      "SQL注入",
		Indicator:     "数据回显",
		PlaybookMatch: "data_echo",
		EvidenceLevel: EvidenceImpactProven,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("impact_proven + PROVE 应 confirmed")
	}
	if res.FinalLevel != EvidenceImpactProven {
		t.Errorf("FinalLevel = %s, want impact_proven", res.FinalLevel)
	}
}

// TestVerifyUnknownCategoryNoPlayback 未知类别无 Playbook → 走通用 4-axis（不查 PROVE/RULE_OUT）。
func TestVerifyUnknownCategoryNoPlaybook(t *testing.T) {
	c := CandidateFinding{
		ID:            "f-7",
		Category:      "某个未知类别",
		Indicator:     "indicator",
		EvidenceLevel: EvidenceCorroborated,
		Axes: []AxisCheck{
			{Name: AxisReal, Passed: true},
			{Name: AxisTriggerable, Passed: true},
			{Name: AxisImpactful, Passed: true},
			{Name: AxisGeneral, Passed: true},
		},
	}
	res := Verify(c)
	if !res.Confirmed {
		t.Errorf("未知类别 + 4-axis 全过 + corroborated 应 confirmed，reason=%s", res.Reason)
	}
	if res.RuleOutHit != "" {
		t.Errorf("未知类别不应命中 RULE_OUT")
	}
}

// TestClassifyDataEcho Classify：有数据回显 → reproduced + 全过。
func TestClassifyDataEcho(t *testing.T) {
	axes, lvl := Classify("SQL注入", "UNION 数据回显", "curl ...", true, true, false, 3)
	if !allAxesPassed(axes) {
		t.Errorf("应全过 4-axis，got %+v", axes)
	}
	if lvl != EvidenceReproduced {
		t.Errorf("lvl = %s, want reproduced", lvl)
	}
}

// TestClassifyOAST Classify：有 OAST → reproduced。
func TestClassifyOAST(t *testing.T) {
	axes, lvl := Classify("SSRF", "OAST 回调", "curl ...", true, false, false, 2)
	if !allAxesPassed(axes) {
		t.Errorf("应全过，got %+v", axes)
	}
	if lvl != EvidenceReproduced {
		t.Errorf("lvl = %s, want reproduced", lvl)
	}
}

// TestClassifyIndicatorOnly Classify：仅单点信号 → corroborated。
func TestClassifyIndicatorOnly(t *testing.T) {
	axes, lvl := Classify("XSS", "反射", "", false, false, false, 2)
	// triggerable 依赖 reproduction 或 OAST/DataEcho/TimeDelay；此处全为 false
	// → triggerable 未过 → 全过为 false，且 lvl 下限 suspected
	if allAxesPassed(axes) {
		t.Errorf("triggerable 应未过")
	}
	if lvl != EvidenceSuspected {
		t.Errorf("lvl = %s, want suspected（triggerable 未过下限）", lvl)
	}
}

// TestClassifyGeneralFail Classify：generalCount<2 → general 未过。
func TestClassifyGeneralFail(t *testing.T) {
	axes, lvl := Classify("XSS", "indicator", "repro", false, true, false, 1)
	// generalCount=1 < 2 → general 未过 → 全过 false → 下限 suspected
	gen := axes[3]
	if gen.Passed {
		t.Errorf("general 应未过（count=1）")
	}
	if allAxesPassed(axes) {
		t.Errorf("应未全过")
	}
	if lvl != EvidenceSuspected {
		t.Errorf("lvl = %s, want suspected（general 未过下限）", lvl)
	}
}

// TestClassifyTimeDelay Classify：时间延迟 → reproduced。
func TestClassifyTimeDelay(t *testing.T) {
	axes, lvl := Classify("SQL注入", "SLEEP 延迟", "curl ...", false, false, true, 2)
	if !allAxesPassed(axes) {
		t.Errorf("应全过，got %+v", axes)
	}
	if lvl != EvidenceReproduced {
		t.Errorf("lvl = %s, want reproduced", lvl)
	}
}
