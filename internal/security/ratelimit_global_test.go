package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestGlobalRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := NewRateLimiter(3, time.Second)
	router := gin.New()
	router.Use(GlobalRateLimitMiddleware(rl))
	router.GET("/api/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/chat/stream", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/mcp", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.10:12345"
		router.ServeHTTP(w, req)
		return w
	}

	// 普通端点：前 3 次通过，第 4 次 429
	for i := 0; i < 3; i++ {
		if w := do("/api/ping"); w.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, w.Code)
		}
	}
	if w := do("/api/ping"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request status = %d, want 429, body=%s", w.Code, w.Body.String())
	}

	// 流式端点豁免：即使普通端点已 429，stream/mcp 路径仍然放行
	if w := do("/api/chat/stream"); w.Code != http.StatusOK {
		t.Fatalf("stream path status = %d, want 200 (exempt)", w.Code)
	}
	if w := do("/api/mcp"); w.Code != http.StatusOK {
		t.Fatalf("mcp path status = %d, want 200 (exempt)", w.Code)
	}

	// Accept: text/event-stream 豁免
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("Accept", "text/event-stream")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SSE accept status = %d, want 200 (exempt)", w.Code)
	}

	// 其他 IP 不受影响
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req2.RemoteAddr = "198.51.100.7:9999"
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("other IP status = %d, want 200", w2.Code)
	}
}
