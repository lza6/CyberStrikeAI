package security

import (
	"context"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

func TestScopeAllows(t *testing.T) {
	cases := []struct {
		name    string
		scope   Scope
		host    string
		port    int
		want    bool
		wantSub string
	}{
		{"nil-ish empty scope allows all", Scope{}, "evil.com", 443, true, ""},
		{"cidr hit", Scope{CIDRs: []string{"192.168.1.0/24"}}, "192.168.1.10", 80, true, ""},
		{"cidr miss", Scope{CIDRs: []string{"192.168.1.0/24"}}, "10.0.0.1", 80, false, "不在允许 CIDR"},
		{"domain exact", Scope{Domains: []string{"example.com"}}, "example.com", 443, true, ""},
		{"domain wildcard sub", Scope{Domains: []string{"*.example.com"}}, "a.b.example.com", 443, true, ""},
		{"domain wildcard miss", Scope{Domains: []string{"*.example.com"}}, "evil.com", 443, false, "不在允许列表"},
		{"port range hit", Scope{Ports: []string{"80,443", "8000-9000"}}, "1.2.3.4", 8080, true, ""},
		{"port range miss", Scope{Ports: []string{"80,443"}}, "1.2.3.4", 8080, false, "端口"},
		{"excluded overrides allow", Scope{CIDRs: []string{"10.0.0.0/8"}, Excluded: []string{"10.1.1.1"}}, "10.1.1.1", 80, false, "排除"},
		{"excluded domain overrides", Scope{Domains: []string{"*.corp.com"}, Excluded: []string{"admin.corp.com"}}, "admin.corp.com", 443, false, "排除"},
		{"port only rule allows any host", Scope{Ports: []string{"443"}}, "anywhere.net", 443, true, ""},
		{"port only rule blocks port", Scope{Ports: []string{"443"}}, "anywhere.net", 80, false, "端口"},
		{"ipv6 cidr hit", Scope{CIDRs: []string{"fe80::/10"}}, "fe80::1", 443, true, ""},
		{"trailing dot domain", Scope{Domains: []string{"example.com"}}, "example.com.", 443, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.scope
			got, reason := s.Allows(tc.host, tc.port)
			if got != tc.want {
				t.Fatalf("Allows(%q,%d)=%v want %v (reason=%q)", tc.host, tc.port, got, tc.want, reason)
			}
			if tc.wantSub != "" && !contains(reason, tc.wantSub) {
				t.Fatalf("reason %q 应包含 %q", reason, tc.wantSub)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestExecutorScopeBlocksOutOfRangeTarget 集成测试：工具 yaml 声明 scope 后，
// 越界目标在 executor.ExecuteTool 层被拦截（不真实执行命令）。
func TestExecutorScopeBlocksOutOfRangeTarget(t *testing.T) {
	logger := zap.NewNop()
	mcpServer := mcp.NewServer(logger)
	cfg := &config.SecurityConfig{
		Tools: []config.ToolConfig{
			{
				Name:    "nmap-scoped",
				Command: "echo",
				Args:    []string{"should-not-run"},
				Enabled: true,
				Scope: &config.ToolScope{
					CIDRs: []string{"192.168.1.0/24"},
				},
			},
		},
	}
	executor := NewExecutor(cfg, mcpServer, logger)
	executor.SetShellSafeEnabled(false)

	// 越界目标：10.0.0.1 不在 192.168.1.0/24
	result, err := executor.ExecuteTool(context.Background(), "nmap-scoped", map[string]interface{}{
		"target": "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("ExecuteTool 返回 err（期望 IsError result）: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("期望越界被拦（IsError=true），got %+v", result)
	}
	if len(result.Content) == 0 || !contains(result.Content[0].Text, "scope") {
		t.Fatalf("错误信息应含 scope，got %+v", result.Content)
	}
}

// TestExtractTarget 常见目标参数形态提取
func TestExtractTarget(t *testing.T) {
	cases := []struct {
		args     map[string]interface{}
		wantHost string
		wantPort int
		wantOK   bool
	}{
		{map[string]interface{}{"target": "192.168.1.1"}, "192.168.1.1", 0, true},
		{map[string]interface{}{"host": "example.com:8080"}, "example.com", 8080, true},
		{map[string]interface{}{"url": "http://10.0.0.5:80/admin"}, "10.0.0.5", 80, true},
		{map[string]interface{}{"domain": "a.example.com"}, "a.example.com", 0, true},
		{map[string]interface{}{"no-target-key": "x"}, "", 0, false},
	}
	for i, tc := range cases {
		host, port, ok := ExtractTarget(tc.args)
		if ok != tc.wantOK || host != tc.wantHost || port != tc.wantPort {
			t.Errorf("case %d: got (%q,%d,%v) want (%q,%d,%v)", i, host, port, ok, tc.wantHost, tc.wantPort, tc.wantOK)
		}
	}
	_ = mcp.Content{} // 保持 import
}
