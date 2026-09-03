package permissions

import "testing"

// TestEvaluateFullAuto 验证 full_auto 放行。
func TestEvaluateFullAuto(t *testing.T) {
	c := New(Settings{Mode: ModeFullAuto})
	d := c.Evaluate("bash", false, "", "rm -rf /tmp")
	if !d.Allowed {
		t.Errorf("full_auto should allow, got %v (%s)", d.Allowed, d.Reason)
	}
}

// TestEvaluateDeniedTools 验证显式拒绝优先级最高。
func TestEvaluateDeniedTools(t *testing.T) {
	c := New(Settings{Mode: ModeFullAuto, DeniedTools: []string{"bash"}})
	d := c.Evaluate("bash", false, "", "")
	if d.Allowed {
		t.Error("denied tool should not be allowed")
	}
}

// TestEvaluateAllowedTools 验证显式允许。
func TestEvaluateAllowedTools(t *testing.T) {
	c := New(Settings{Mode: ModeDefault, AllowedTools: []string{"read_file"}})
	d := c.Evaluate("read_file", false, "", "")
	if !d.Allowed {
		t.Error("allowed tool should be allowed")
	}
}

// TestEvaluateReadOnly 验证只读工具放行。
func TestEvaluateReadOnly(t *testing.T) {
	c := New(Settings{Mode: ModeDefault})
	d := c.Evaluate("grep", true, "", "")
	if !d.Allowed {
		t.Error("read-only should be allowed")
	}
}

// TestEvaluatePlanMode 验证 plan 模式阻塞变更工具。
func TestEvaluatePlanMode(t *testing.T) {
	c := New(Settings{Mode: ModePlan})
	d := c.Evaluate("write_file", false, "", "")
	if d.Allowed {
		t.Error("plan mode should block mutating tools")
	}
}

// TestEvaluateDefaultRequiresConfirmation 验证 default 需确认。
func TestEvaluateDefaultRequiresConfirmation(t *testing.T) {
	c := New(Settings{Mode: ModeDefault})
	d := c.Evaluate("write_file", false, "", "")
	if d.Allowed {
		t.Error("default should not allow mutating without confirmation")
	}
	if !d.RequiresConfirmation {
		t.Error("default should require confirmation")
	}
}

// TestEvaluatePathRuleDeny 验证 path deny 规则。
func TestEvaluatePathRuleDeny(t *testing.T) {
	c := New(Settings{
		Mode: ModeFullAuto,
		PathRules: []PathRule{
			{Pattern: "*.tmp", Allow: false},
		},
	})
	d := c.Evaluate("write_file", false, "foo.tmp", "")
	if d.Allowed {
		t.Error("deny path rule should block")
	}
	if d.Reason == "" {
		t.Error("deny reason should not be empty")
	}
}

// TestEvaluatePathRuleAllow 验证 path allow 不阻断（即使匹配 deny 前缀，allow 不覆盖 deny）。
func TestEvaluatePathRuleAllowOnly(t *testing.T) {
	c := New(Settings{
		Mode: ModeFullAuto,
		PathRules: []PathRule{
			{Pattern: "/safe/**", Allow: true},
		},
	})
	// 没有匹配的 deny 规则，full_auto 放行
	d := c.Evaluate("write_file", false, "/safe/file.txt", "")
	if !d.Allowed {
		t.Error("full_auto + allow path rule should allow")
	}
}

// TestEvaluateCommandDeny 验证 command deny pattern。
func TestEvaluateCommandDeny(t *testing.T) {
	c := New(Settings{
		Mode: ModeFullAuto,
		DeniedCommands: []string{"rm -rf *"},
	})
	d := c.Evaluate("bash", false, "", "rm -rf /")
	if d.Allowed {
		t.Error("command deny should block")
	}
}

// TestMatchGlob 验证 glob 匹配语义。
func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pat, name string
		ok        bool
	}{
		{"*.tmp", "foo.tmp", true},
		{"*.tmp", "foo.txt", false},
		{"foo*", "foobar", true},
		{"foo*", "barfoo", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"[abc].txt", "a.txt", true},
		{"[abc].txt", "d.txt", false},
		{"[!abc].txt", "d.txt", true},
		{"[!abc].txt", "a.txt", false},
		{"[a-z].txt", "m.txt", true},
		{"[a-z].txt", "1.txt", false},
		// fnmatch 的 * 跨 /（与 path.Match 不同）
		{"safe/*", "safe/a/b", true},
		{"*", "anything/with/slash", true},
		// 空模式不匹配非空
		{"", "x", false},
		{"*", "", true},
		// L1 修复验证：] 在字符类首位按字面量处理（fnmatch 语义）
		{"[]]", "]", true},
		{"a[]]b", "a]b", true},
		{"[]a]", "a", true},
		{"[]a]", "b", false},
		{"[!]]", "a", true},
		{"[!]]", "]", false},
	}
	for _, tt := range tests {
		got := matchGlob(tt.pat, tt.name)
		if got != tt.ok {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pat, tt.name, got, tt.ok)
		}
	}
}
