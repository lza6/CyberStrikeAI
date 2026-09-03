package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/blackboard"
	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func newHealthTestRouter(t *testing.T) (*gin.Engine, *database.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// Windows：t.TempDir 清理前必须关库句柄，否则 unlinkat 撞文件锁。
	db, err := database.NewDB(filepath.Join(t.TempDir(), "health-test.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := gin.New()
	h := NewHealthHandler(time.Now(), "vtest", db, nil, blackboard.NewMemoryBoard(zap.NewNop()))
	router.GET("/healthz", h.Healthz)
	router.GET("/readyz", h.Readyz)
	return router, db
}

// TestHealthzLiveness liveness 恒 200，不触碰依赖；验证 JSON 结构（status/uptime/version）。
func TestHealthzLiveness(t *testing.T) {
	router, _ := newHealthTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status want ok, got %v", body["status"])
	}
	if uptime, ok := body["uptime"].(string); !ok || uptime == "" {
		t.Errorf("uptime want 非空字符串, got %v", body["uptime"])
	}
	if body["version"] != "vtest" {
		t.Errorf("version want vtest, got %v", body["version"])
	}
}

// TestHealthzEmptyVersionFallback version 为空回退 "unknown"。
func TestHealthzEmptyVersionFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewHealthHandler(time.Now(), "", nil, nil, nil)
	router.GET("/healthz", h.Healthz)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["version"] != "unknown" {
		t.Errorf("version want unknown, got %v", body["version"])
	}
}

// TestReadyzAllReady DB/黑板均就绪时 200 + status=ready + 各检查项 ok/skipped。
func TestReadyzAllReady(t *testing.T) {
	router, _ := newHealthTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Status != "ready" {
		t.Errorf("status want ready, got %s (checks=%v)", body.Status, body.Checks)
	}
	if body.Checks["db"] != "ok" {
		t.Errorf("checks.db want ok, got %q", body.Checks["db"])
	}
	if body.Checks["blackboard"] != "ok" {
		t.Errorf("checks.blackboard want ok, got %q", body.Checks["blackboard"])
	}
	// knowledgeDB 为 nil（未启用知识库）→ skipped，不得拖累整体 ready。
	if body.Checks["knowledge"] != "skipped" {
		t.Errorf("checks.knowledge want skipped, got %q", body.Checks["knowledge"])
	}
}

// TestReadyzDegradedWhenDBFails 主库不可用（Ping 失败/未配置）时 503 + status=degraded。
func TestReadyzDegradedWhenDBFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	// db=nil：模拟依赖未配置的失败态
	h := NewHealthHandler(time.Now(), "vtest", nil, nil, nil)
	router.GET("/readyz", h.Readyz)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Status != "degraded" {
		t.Errorf("status want degraded, got %s", body.Status)
	}
	if !strings.HasPrefix(body.Checks["db"], "fail:") {
		t.Errorf("checks.db want fail: 前缀, got %q", body.Checks["db"])
	}
}

// TestReadyzDegradedWhenKbdbFails 独立知识库库 Ping 失败时同样 503 degraded。
func TestReadyzDegradedWhenKbdbFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "health-kb-test.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 独立构造一个知识库库并 Close，模拟不可达（PingContext 必失败）。
	kb, err := database.NewDB(filepath.Join(t.TempDir(), "health-kb-closed.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB(kb): %v", err)
	}
	if err := kb.Close(); err != nil {
		t.Fatalf("close kb: %v", err)
	}
	closedKB := kb
	router := gin.New()
	h := NewHealthHandler(time.Now(), "vtest", db, closedKB, nil)
	router.GET("/readyz", h.Readyz)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d, body=%s", rec.Code, rec.Body.String())
	}
}
