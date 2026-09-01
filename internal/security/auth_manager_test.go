package security

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestAuthManagerAuthenticatesCreatedRBACUser(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "auth-rbac.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	manager := NewAuthManager(12)
	if _, err := manager.AttachRBACStore(db); err != nil {
		t.Fatalf("AttachRBACStore: %v", err)
	}
	hash, err := HashPassword("operator-secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := db.CreateRBACUser("operator1", "Operator One", hash, true, []string{database.RBACSystemRoleViewer})
	if err != nil {
		t.Fatalf("CreateRBACUser: %v", err)
	}

	token, _, err := manager.Authenticate("operator1", "operator-secret")
	if err != nil {
		t.Fatalf("Authenticate created user: %v", err)
	}
	session, ok := manager.ValidateToken(token)
	if !ok {
		t.Fatalf("expected created user session to validate")
	}
	if session.UserID != user.ID || session.Username != "operator1" {
		t.Fatalf("session user = %s/%s, want %s/operator1", session.UserID, session.Username, user.ID)
	}
	if !session.Permissions["auth:self"] || !session.Permissions["chat:read"] {
		t.Fatalf("expected viewer permissions in session, got %#v", session.Permissions)
	}

	if _, _, err := manager.Authenticate("", "operator-secret"); err == nil {
		t.Fatalf("empty username must not authenticate non-admin user")
	}

	router := gin.New()
	router.Use(AuthMiddleware(manager))
	router.GET("/principal", func(c *gin.Context) {
		principal, ok := authctx.PrincipalFromContext(c.Request.Context())
		if !ok || principal.UserID != user.ID || !principal.HasPermission("chat:read") || principal.ScopeFor("chat:read") != database.RBACScopeAssigned {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/principal", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("principal propagation status = %d", w.Code)
	}
}

func TestQueryTokenOnlyAllowedForSSEAndWebSocketGET(t *testing.T) {
	requestToken := func(method, accept, upgrade string) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(method, "/api/test?token=secret", nil)
		c.Request.Header.Set("Accept", accept)
		c.Request.Header.Set("Upgrade", upgrade)
		return extractTokenFromRequest(c)
	}
	if got := requestToken(http.MethodGet, "application/json", ""); got != "" {
		t.Fatalf("ordinary GET accepted query token %q", got)
	}
	if got := requestToken(http.MethodPost, "text/event-stream", ""); got != "" {
		t.Fatalf("POST accepted query token %q", got)
	}
	if got := requestToken(http.MethodGet, "text/event-stream", ""); got != "secret" {
		t.Fatalf("SSE token = %q", got)
	}
	if got := requestToken(http.MethodGet, "", "websocket"); got != "secret" {
		t.Fatalf("WebSocket token = %q", got)
	}
}

func TestSessionLimitEviction(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "auth-limit.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	manager := NewAuthManagerWithLimit(12, 3)
	if _, err := manager.AttachRBACStore(db); err != nil {
		t.Fatalf("AttachRBACStore: %v", err)
	}
	hash, err := HashPassword("operator-secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	// 预建 4 个用户，逐个登录产生可区分的会话
	usernames := []string{"user1", "user2", "user3", "user4"}
	for _, name := range usernames {
		if _, err := db.CreateRBACUser(name, name, hash, true, []string{database.RBACSystemRoleViewer}); err != nil {
			t.Fatalf("CreateRBACUser %s: %v", name, err)
		}
	}

	tokens := make([]string, 0, len(usernames))
	for _, name := range usernames {
		token, _, err := manager.Authenticate(name, "operator-secret")
		if err != nil {
			t.Fatalf("Authenticate %s: %v", name, err)
		}
		tokens = append(tokens, token)
	}

	manager.mu.RLock()
	sessionCount := len(manager.sessions)
	manager.mu.RUnlock()
	if sessionCount > 3 {
		t.Fatalf("sessions map size = %d, want <= 3", sessionCount)
	}

	// 最早的会话（user1）应被驱逐，最近的会话（user4）应仍然有效
	if _, ok := manager.ValidateToken(tokens[0]); ok {
		t.Fatalf("earliest session token should have been evicted")
	}
	if _, ok := manager.ValidateToken(tokens[3]); !ok {
		t.Fatalf("latest session token should still validate")
	}
	if _, ok := manager.ValidateToken(tokens[2]); !ok {
		t.Fatalf("second-latest session token should still validate")
	}
}
