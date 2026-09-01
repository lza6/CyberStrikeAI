package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupSecureHeadersRouter(isTLS bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecureHeaders(isTLS))
	router.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
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
