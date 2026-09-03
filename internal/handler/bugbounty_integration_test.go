package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TestBugBounty_Export_HTTPEntry_WithDB 修复 M2：用真实 DB（sqlite_pure_go）走 Export 完整 HTTP 路径，
// 验证 Count → List → access 过滤 → respondEmpty/export 各分支的 RBAC 范围一致性。
func TestBugBounty_Export_HTTPEntry_WithDB(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "bugbounty-export.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 建对话（会话所属用户 u1）+ 两条漏洞（u1 的会话，含 critical + high）
	conv, err := db.CreateConversation("bugbounty-e2e", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.SetResourceOwner("conversation", conv.ID, "u1")
	_, err = db.CreateVulnerability(&database.Vulnerability{
		ConversationID: conv.ID, Title: "SQL Injection in search", Severity: "critical", Type: "SQL注入", Target: "acme.corp",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateVulnerability(&database.Vulnerability{
		ConversationID: conv.ID, Title: "Stored XSS in comments", Severity: "high", Target: "acme.corp",
	})
	if err != nil {
		t.Fatal(err)
	}

	vh := NewVulnerabilityHandler(db, zap.NewNop())
	h := NewBugBountyHandler(vh, zap.NewNop())

	router := gin.New()
	router.Use(func(c *gin.Context) {
		// 模拟 local_mode：u1 全权限（RBACScopeAll）
		c.Set(security.ContextSessionKey, security.Session{UserID: "u1", Scope: database.RBACScopeAll})
		c.Next()
	})
	router.GET("/api/bugbounty/report", h.Export)

	// 1) format=bounty：应返回 2 条 + 总区间
	req := httptest.NewRequest("GET", "/api/bugbounty/report?format=bounty", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bounty status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if int(resp["total"].(float64)) != 2 {
		t.Errorf("total=%v want 2", resp["total"])
	}
	total, _ := resp["bounty_total"].(map[string]interface{})
	if total == nil {
		t.Fatal("bounty_total 缺失")
	}
	// critical(1500-10000) + high(500-3000) = 2000-13000
	if int(total["low_usd"].(float64)) != 2000 {
		t.Errorf("low_usd=%v want 2000", total["low_usd"])
	}

	// 2) format=roi&spend=10：critical 公开市场 low=1500（u1 全权限可见）+ high=500 → low 合计 2000
	//    spend=10 → ratio_low=200 > 10 → green
	req = httptest.NewRequest("GET", "/api/bugbounty/report?format=roi&spend=10", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("roi status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("roi JSON 解析失败: %v", err)
	}
	if resp["verdict"] != "green" {
		t.Errorf("verdict=%v want green (2000/10=200x)", resp["verdict"])
	}

	// 3) format=dedup&threshold=0.4：应识别 v1/v2（标题不同，可能不重复）+ JSON 结构合法
	req = httptest.NewRequest("GET", "/api/bugbounty/report?format=dedup&threshold=0.4&k=3", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dedup status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("dedup JSON 解析失败: %v", err)
	}
	if resp["format"] != "dedup" {
		t.Errorf("format=%v want dedup", resp["format"])
	}

	// 4) format=report：Markdown 聚合
	req = httptest.NewRequest("GET", "/api/bugbounty/report?format=report&spend=10", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("report status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "# 漏洞赏金与 ROI 报告") {
		t.Errorf("report 应以标题开头:\n%s", body[:min(len(body), 40)])
	}

	// 5) invalid format → 400（不在 Count 之前静默 200）
	req = httptest.NewRequest("GET", "/api/bugbounty/report?format=invalid", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid format status=%d want 400", w.Code)
	}

	// 6) 空范围（conversation_id 不存在）→ respondEmpty 200 + total:0
	req = httptest.NewRequest("GET", "/api/bugbounty/report?format=bounty&conversation_id=nonexistent", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("empty status=%d want 200", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("empty JSON 解析失败: %v", err)
	}
	if int(resp["total"].(float64)) != 0 {
		t.Errorf("empty total=%v want 0", resp["total"])
	}
}
