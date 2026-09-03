package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// TestScopeFromProjectJSONString 验证 project.scope_json 解析为可硬拦的 Scope。
func TestScopeFromProjectJSONString(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Scope
	}{
		{
			name: "empty=不限",
			raw:  "",
			want: Scope{},
		},
		{
			name: "非法JSON=不限",
			raw:  "{not json",
			want: Scope{},
		},
		{
			name: "targets CIDR+域名+URL",
			raw:  `{"targets":["192.168.1.0/24","example.com","https://10.0.0.5:80/admin"]}`,
			want: Scope{
				CIDRs:   []string{"192.168.1.0/24"},
				Domains: []string{"example.com", "10.0.0.5"},
				Ports:   []string{"80"},
			},
		},
		{
			name: "exclude 域名",
			raw:  `{"targets":["10.0.0.0/8"],"exclude":["admin.corp.com","10.1.1.1"]}`,
			want: Scope{
				CIDRs:    []string{"10.0.0.0/8"},
				Excluded: []string{"admin.corp.com", "10.1.1.1"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeFromProjectJSONString(tc.raw)
			if !sliceEqual(got.CIDRs, tc.want.CIDRs) {
				t.Errorf("CIDRs=%v want %v", got.CIDRs, tc.want.CIDRs)
			}
			if !sliceEqual(got.Domains, tc.want.Domains) {
				t.Errorf("Domains=%v want %v", got.Domains, tc.want.Domains)
			}
			if !sliceEqual(got.Ports, tc.want.Ports) {
				t.Errorf("Ports=%v want %v", got.Ports, tc.want.Ports)
			}
			if !sliceEqual(got.Excluded, tc.want.Excluded) {
				t.Errorf("Excluded=%v want %v", got.Excluded, tc.want.Excluded)
			}
		})
	}
}

// TestScopeFromProject 集成：从真实 db 读 scope_json。
func TestScopeFromProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scope-block.db")
	db, err := database.NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Skipf("跳过（CGO/sqlite 不可用）: %v", err)
	}
	defer db.Close() // 释放 sqlite 句柄，避免 Windows TempDir cleanup 失败
	proj := &database.Project{
		Name:      "授权项目",
		ScopeJSON: `{"targets":["192.168.1.0/24","example.com"],"exclude":["10.1.1.1"]}`,
		Status:    "active",
	}
	created, err := db.CreateProject(proj)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s := ScopeFromProject(db, created.ID)
	if len(s.CIDRs) != 1 || s.CIDRs[0] != "192.168.1.0/24" {
		t.Fatalf("CIDRs 不符: %+v", s.CIDRs)
	}
	if len(s.Domains) != 1 || s.Domains[0] != "example.com" {
		t.Fatalf("Domains 不符: %+v", s.Domains)
	}
	if len(s.Excluded) != 1 || s.Excluded[0] != "10.1.1.1" {
		t.Fatalf("Excluded 不符: %+v", s.Excluded)
	}
}

// TestExecuteScopeGuard_CheckExecute 验证 execute 命令越界被拦。
func TestExecuteScopeGuard_CheckExecute(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scope-block-guard.db")
	db, err := database.NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Skipf("跳过（CGO/sqlite 不可用）: %v", err)
	}
	defer db.Close()
	proj, _ := db.CreateProject(&database.Project{
		Name:      "scope",
		ScopeJSON: `{"targets":["192.168.1.0/24"]}`,
		Status:    "active",
	})
	guard := &ExecuteScopeGuard{
		Resolve: func(pid string) Scope { return ScopeFromProject(db, pid) },
		Logger:  zap.NewNop(),
	}
	// 越界目标被拦
	ctx := mcp.WithMCPProjectID(context.Background(), proj.ID)
	hint, blocked := guard.CheckExecute(ctx, "execute", map[string]interface{}{"command": "nmap -sV 10.0.0.1"}, false)
	if !blocked || hint == "" {
		t.Fatalf("越界应被拦，got blocked=%v hint=%q", blocked, hint)
	}
	// 授权内目标放行
	hint2, blocked2 := guard.CheckExecute(ctx, "execute", map[string]interface{}{"command": "nmap -sV 192.168.1.10"}, false)
	if blocked2 {
		t.Fatalf("授权内不应拦，got blocked=%v hint=%q", blocked2, hint2)
	}
	// 未绑定 project 放行
	hint3, blocked3 := guard.CheckExecute(context.Background(), "execute", map[string]interface{}{"command": "nmap 10.0.0.1"}, false)
	if blocked3 {
		t.Fatalf("未绑定 project 不应拦，got blocked=%v hint=%q", blocked3, hint3)
	}
}

// TestExecutorProjectScopeBlocksOutOfRangeTarget J4 集成：executor + project scope 硬闸。
func TestExecutorProjectScopeBlocksOutOfRangeTarget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scope-exec.db")
	db, err := database.NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Skipf("跳过（CGO/sqlite 不可用）: %v", err)
	}
	defer db.Close()
	proj, _ := db.CreateProject(&database.Project{
		Name:      "授权",
		ScopeJSON: `{"targets":["192.168.1.0/24"]}`,
		Status:    "active",
	})
	_ = proj
	logger := zap.NewNop()
	mcpServer := mcp.NewServer(logger)
	cfg := &config.SecurityConfig{
		Tools: []config.ToolConfig{
			{
				Name:    "nmap-scoped",
				Command: "echo",
				Args:    []string{"should-not-run"},
				Enabled: true,
				Parameters: []config.ParameterConfig{
					{Name: "target", Type: "string", Required: true, Position: intPtr(1), Format: "positional"},
				},
			},
		},
	}
	executor := NewExecutor(cfg, mcpServer, logger)
	executor.SetShellSafeEnabled(false)
	executor.SetProjectScopeResolver(projectScopeResolverFunc(func(pid string) Scope {
		return ScopeFromProject(db, pid)
	}))
	// 192.168.1.0/24 授权，越界 10.0.0.1 应被 project scope 拦
	proj2, _ := db.CreateProject(&database.Project{
		Name:      "授权2",
		ScopeJSON: `{"targets":["192.168.1.0/24"]}`,
		Status:    "active",
	})
	ctx2 := mcp.WithMCPProjectID(context.Background(), proj2.ID)
	result, err := executor.ExecuteTool(ctx2, "nmap-scoped", map[string]interface{}{"target": "10.0.0.1"})
	if err != nil {
		t.Fatalf("ExecuteTool err（期望 IsError result）: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("期望越界被 project scope 拦，got %+v", result)
	}
	if len(result.Content) == 0 || !contains(result.Content[0].Text, "项目授权范围") {
		t.Fatalf("错误信息应含项目授权范围，got %+v", result.Content)
	}
	// 授权内目标应执行（echo 输出 should-not-run）
	result2, err := executor.ExecuteTool(ctx2, "nmap-scoped", map[string]interface{}{"target": "192.168.1.10"})
	if err != nil || (result2 != nil && result2.IsError) {
		t.Fatalf("授权内不应拦，got err=%v result=%+v", err, result2)
	}
}

// projectScopeResolverFunc 适配器：函数 → projectScopeResolver 接口。
type projectScopeResolverFunc func(projectID string) Scope

func (f projectScopeResolverFunc) ResolveProjectScope(projectID string) Scope {
	if f == nil {
		return Scope{}
	}
	return f(projectID)
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func intPtr(v int) *int { return &v }

// 占位：保证 import 不被编译器判定未使用。
var _ = os.Getenv
