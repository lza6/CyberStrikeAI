package authctx

import (
	"context"
	"strings"
	"testing"
)

func perms(ps ...string) map[string]bool {
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[p] = true
	}
	return m
}

// TestNewPrincipalCopiesPermissions 确认构造器深拷贝权限，不共享底层 map。
func TestNewPrincipalCopiesPermissions(t *testing.T) {
	src := perms("scan", "exploit")
	p := NewPrincipal("u1", "alice", "proj-a", src)
	src["scan"] = false // 修改原 map 不应影响 Principal
	if !p.HasPermission("scan") {
		t.Fatal("构造器应深拷贝权限 map")
	}
}

// TestNewPrincipalTrimsWhitespace 确认 userID/username/scope 被 TrimSpace。
func TestNewPrincipalTrimsWhitespace(t *testing.T) {
	p := NewPrincipal("  u1  ", "  alice  ", "  proj-a  ", perms("scan"))
	if p.UserID != "u1" || p.Username != "alice" || p.Scope != "proj-a" {
		t.Fatalf("未 trim: %+v", p)
	}
}

// TestNewPrincipalWithScopesOnlyKeepsAllowed 确认仅保留允许权限的 scope，且 copy 独立。
func TestNewPrincipalWithScopesOnlyKeepsAllowed(t *testing.T) {
	srcScopes := map[string]string{"scan": "proj-a", "exploit": "proj-b", "admin": "all"}
	srcPerms := map[string]bool{"scan": true, "exploit": false, "admin": true}
	p := NewPrincipalWithScopes("u", "bob", "all", srcPerms, srcScopes)
	// exploit 被禁用：不进入 PermissionScopes
	if p.HasPermission("exploit") {
		t.Fatal("exploit 不应被授权")
	}
	if got := p.ScopeFor("scan"); got != "proj-a" {
		t.Fatalf("scan scope = %q, want proj-a", got)
	}
	if got := p.ScopeFor("admin"); got != "all" {
		t.Fatalf("admin scope = %q, want all", got)
	}
	// 修改源 map 不应影响
	srcScopes["scan"] = "mutated"
	if got := p.ScopeFor("scan"); got != "proj-a" {
		t.Fatalf("scope 应深拷贝，got %q", got)
	}
}

// TestWithPrincipalSkipsEmptyUser 确认空 userID 不写入 context。
func TestWithPrincipalSkipsEmptyUser(t *testing.T) {
	ctx := context.Background()
	out := WithPrincipal(ctx, Principal{UserID: "  "})
	if _, ok := PrincipalFromContext(out); ok {
		t.Fatal("空 userID 不应写入 context")
	}
}

// TestPrincipalRoundTrip 确认写读往返一致。
func TestPrincipalRoundTrip(t *testing.T) {
	p := NewPrincipalWithScopes("u9", "carol", "scope-x", perms("scan", "read"), map[string]string{"scan": "scope-x"})
	ctx := WithPrincipal(context.Background(), p)
	got, ok := PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("往返后应可取回 principal")
	}
	if got.UserID != "u9" || got.Username != "carol" || !got.HasPermission("read") {
		t.Fatalf("往返不一致: %+v", got)
	}
}

// TestNilContextSafe 确认 nil context 不 panic。
func TestNilContextSafe(t *testing.T) {
	if _, ok := PrincipalFromContext(nil); ok {
		t.Fatal("nil context 不应有 principal")
	}
	out := WithPrincipal(nil, NewPrincipal("u", "d", "s", perms("r")))
	if out == nil {
		t.Fatal("nil context 应回退 Background")
	}
}

// TestHasPermissionTrimInput 确认权限查询 trim 入参。
func TestHasPermissionTrimInput(t *testing.T) {
	p := NewPrincipal("u", "e", "s", perms("scan"))
	if !p.HasPermission("  scan  ") {
		t.Fatal("HasPermission 应 trim 入参")
	}
}

// TestScopeForFallsBackToScope 确认无 per-scope 时回退全局 Scope。
func TestScopeForFallsBackToScope(t *testing.T) {
	p := NewPrincipal("u", "f", "global-scope", perms("scan"))
	if got := p.ScopeFor("scan"); got != "global-scope" {
		t.Fatalf("应回退全局 scope, got %q", got)
	}
}

// TestPrincipalEmptyPermissionsSafe 确认空权限不 panic。
func TestPrincipalEmptyPermissionsSafe(t *testing.T) {
	p := NewPrincipal("u", "g", "s", nil)
	if p.HasPermission("anything") || p.HasPermission("") {
		t.Fatal("空权限应全 false")
	}
	if strings.TrimSpace(p.Scope) != "s" {
		t.Fatal("scope 保留")
	}
}
