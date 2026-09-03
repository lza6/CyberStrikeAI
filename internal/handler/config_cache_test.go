package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/cache"
	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// hitCountingCache 包装一个 Cache，统计 Get 命中次数，用于断言 cache-aside 行为。
type hitCountingCache struct {
	inner cache.Cache
	hits  int32
}

func (c *hitCountingCache) Get(ctx context.Context, key string) ([]byte, bool) {
	v, ok := c.inner.Get(ctx, key)
	if ok {
		atomic.AddInt32(&c.hits, 1)
	}
	return v, ok
}

func (c *hitCountingCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	c.inner.Set(ctx, key, val, ttl)
}

func (c *hitCountingCache) Delete(ctx context.Context, key string) {
	c.inner.Delete(ctx, key)
}

// newCacheAsideTestHandler 构造最小 ConfigHandler：无 mcpServer/externalMCPMgr/agent，
// Security.Tools 仅含静态工具，便于通过 body 内容断言缓存命中 vs 未命中。
func newCacheAsideTestHandler(t *testing.T) (*ConfigHandler, *hitCountingCache) {
	t.Helper()
	cfg := config.Default()
	cfg.Security.Tools = []config.ToolConfig{
		{Name: "cache-tool-a", Enabled: true, ShortDescription: "A", Description: "tool A for cache test"},
	}
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: \"1\"\n"), 0o600); err != nil {
		t.Fatalf("写临时 config 失败: %v", err)
	}
	h := NewConfigHandler(configPath, cfg, nil, nil, nil, nil, nil, zap.NewNop())
	cc := &hitCountingCache{inner: cache.NewMemoryCache(context.Background(), 0)}
	h.SetCache(cc)
	return h, cc
}

// TestGetConfigCacheHitSecondCallReturnsCachedBody 第二次 GET /api/config 命中 cache，
// 即使中途修改 config（不 invalidate），返回 body 仍是第一次的快照（证明 cache 生效）。
func TestGetConfigCacheHitSecondCallReturnsCachedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, cc := newCacheAsideTestHandler(t)
	r := gin.New()
	r.GET("/api/config", h.GetConfig)

	// 第一次：未命中 → marshal + Set cache
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("第一次 GET 期望 200，实际 %d", w1.Code)
	}
	body1 := w1.Body.Bytes()
	if !bytes.Contains(body1, []byte("cache-tool-a")) {
		t.Fatalf("第一次 body 应包含 cache-tool-a，got: %s", body1)
	}
	if got := atomic.LoadInt32(&cc.hits); got != 0 {
		t.Fatalf("第一次应未命中 cache（hits=0），实际 hits=%d", got)
	}

	// 中途修改 config（不调 invalidate）：追加第二个工具。
	// 若第二次请求命中 cache，body 应仍只含 cache-tool-a（旧快照）。
	h.mu.Lock()
	h.config.Security.Tools = append(h.config.Security.Tools, config.ToolConfig{
		Name: "cache-tool-b", Enabled: true, ShortDescription: "B", Description: "tool B",
	})
	h.mu.Unlock()

	// 第二次：命中 cache → 返回 body1（不含 cache-tool-b）
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("第二次 GET 期望 200，实际 %d", w2.Code)
	}
	body2 := w2.Body.Bytes()
	if !bytes.Equal(body1, body2) {
		t.Fatalf("第二次应命中 cache 返回相同 body；got diff (len %d vs %d)", len(body1), len(body2))
	}
	if bytes.Contains(body2, []byte("cache-tool-b")) {
		t.Fatal("命中 cache 时不应包含后追加的 cache-tool-b")
	}
	if got := atomic.LoadInt32(&cc.hits); got != 1 {
		t.Fatalf("第二次应命中 cache（hits=1），实际 hits=%d", got)
	}

	// 失效后第三次：重新 marshal → 含 cache-tool-b
	h.invalidateConfigCache()
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("第三次 GET 期望 200，实际 %d", w3.Code)
	}
	body3 := w3.Body.Bytes()
	if !bytes.Contains(body3, []byte("cache-tool-b")) {
		t.Fatalf("invalidate 后应含 cache-tool-b，got: %s", body3)
	}
}

// TestGetConfigNoStoreHeader 校验 GetConfig 回 Cache-Control: no-store，避免客户端/代理缓存。
func TestGetConfigNoStoreHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newCacheAsideTestHandler(t)
	r := gin.New()
	r.GET("/api/config", h.GetConfig)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	cc := w.Header().Get("Cache-Control")
	if cc != "no-store, no-cache, must-revalidate" {
		t.Fatalf("期望 Cache-Control=no-store, no-cache, must-revalidate，实际 %q", cc)
	}
}

// TestInvalidateConfigCacheNilSafe cache=nil 时 invalidate 不 panic。
func TestInvalidateConfigCacheNilSafe(t *testing.T) {
	h, _ := newCacheAsideTestHandler(t)
	h.configCache = nil // 显式置空
	// 不应 panic
	h.invalidateConfigCache()
}
