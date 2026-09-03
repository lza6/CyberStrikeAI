package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupSecureHeadersRouter(isTLS bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecureHeaders(isTLS))
	router.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/static/js/foo.js", func(c *gin.Context) { c.Status(http.StatusOK) })
	return router
}

func TestSecureHeaders(t *testing.T) {
	assertHeaders := func(t *testing.T, w *httptest.ResponseRecorder, wantHSTS bool) {
		t.Helper()
		headers := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
			"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
		}
		for name, want := range headers {
			if got := w.Header().Get(name); got != want {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		csp := w.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("Content-Security-Policy header missing")
		}
		for _, directive := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
			found := false
			for _, part := range splitCSPDirectives(csp) {
				if part == directive {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("CSP missing directive %q, got %q", directive, csp)
			}
		}
		hsts := w.Header().Get("Strict-Transport-Security")
		if wantHSTS && hsts == "" {
			t.Errorf("HTTPS mode: Strict-Transport-Security header missing")
		}
		if !wantHSTS && hsts != "" {
			t.Errorf("HTTP mode: unexpected Strict-Transport-Security header %q", hsts)
		}
	}

	t.Run("http mode no HSTS", func(t *testing.T) {
		router := setupSecureHeadersRouter(false)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
		assertHeaders(t, w, false)
	})

	t.Run("https mode with HSTS", func(t *testing.T) {
		router := setupSecureHeadersRouter(true)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
		assertHeaders(t, w, true)
	})

	t.Run("non-static path gets no-store Cache-Control", func(t *testing.T) {
		router := setupSecureHeadersRouter(false)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
		cc := w.Header().Get("Cache-Control")
		if cc != "no-store, no-cache, must-revalidate" {
			t.Fatalf("非静态路径期望 Cache-Control=no-store..., 实际 %q", cc)
		}
	})

	t.Run("CSP script-src uses nonce not unsafe-inline", func(t *testing.T) {
		router := setupSecureHeadersRouter(false)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
		csp := w.Header().Get("Content-Security-Policy")
		if strings.Contains(csp, "'unsafe-inline'") && strings.Contains(csp, "script-src") {
			// script-src 段不允许 unsafe-inline（style-src 保留）
			for _, part := range splitCSPDirectives(csp) {
				if strings.HasPrefix(part, "script-src") && strings.Contains(part, "unsafe-inline") {
					t.Errorf("script-src 不应包含 unsafe-inline（F4 nonce 化已收紧）: %q", part)
				}
			}
		}
		if !strings.Contains(csp, "'nonce-") {
			t.Errorf("CSP script-src 应包含 nonce 指令: %q", csp)
		}
	})

	t.Run("nonce unique per request and hex32", func(t *testing.T) {
		router := setupSecureHeadersRouter(false)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/ping", nil))
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/ping", nil))
		n1 := extractNonce(w1.Header().Get("Content-Security-Policy"))
		n2 := extractNonce(w2.Header().Get("Content-Security-Policy"))
		if n1 == "" || n2 == "" {
			t.Fatalf("nonce 提取失败: %q / %q", n1, n2)
		}
		if len(n1) != 32 {
			t.Errorf("nonce 应为 32 hex 字符, got %d", len(n1))
		}
		if n1 == n2 {
			t.Errorf("每请求 nonce 应唯一, 两次相同: %s", n1)
		}
		// context 注入一致性
		router2 := gin.New()
		router2.Use(SecureHeaders(false))
		var ctxNonce string
		router2.GET("/ctx", func(c *gin.Context) {
			ctxNonce = CSPNonceFromContext(c)
			c.Status(http.StatusOK)
		})
		w3 := httptest.NewRecorder()
		router2.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/ctx", nil))
		if ctxNonce != extractNonce(w3.Header().Get("Content-Security-Policy")) {
			t.Errorf("context nonce 与 CSP 头 nonce 不一致")
		}
	})

	t.Run("static path does not get no-store Cache-Control", func(t *testing.T) {
		router := setupSecureHeadersRouter(false)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/js/foo.js", nil))
		// SecureHeaders 不应为 /static/ 设 no-store（由 StaticCacheHeaders 设长缓存）
		if cc := w.Header().Get("Cache-Control"); cc == "no-store, no-cache, must-revalidate" {
			t.Fatalf("/static/ 不应被 SecureHeaders 设 no-store（应由 StaticCacheHeaders 设长缓存）")
		}
	})
}

// extractNonce 从 CSP 头提取 'nonce-xxx' 中的 xxx
func extractNonce(csp string) string {
	i := strings.Index(csp, "'nonce-")
	if i < 0 {
		return ""
	}
	rest := csp[i+len("'nonce-"):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// splitCSPDirectives 按分号拆分并去空格，便于指令级断言
func splitCSPDirectives(csp string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(csp); i++ {
		if i == len(csp) || csp[i] == ';' {
			part := trimSpaces(csp[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trimSpaces(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}
