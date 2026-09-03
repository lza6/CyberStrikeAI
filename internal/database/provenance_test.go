package database

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// 验证迁移项 4：provenance 列幂等加列 + GetProvenance 组装。
// 这些测试需要 CGO（NewDB 用 sqlite3），运行需 mingw gcc + CGO_ENABLED=1。

func TestMigrateVulnerabilitiesProvenance_idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "prov.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 再调一次，应幂等不报错
	if err := db.MigrateVulnerabilitiesProvenance(); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	// 第三次
	if err := db.MigrateVulnerabilitiesProvenance(); err != nil {
		t.Fatalf("third migrate failed: %v", err)
	}

	// 验证列存在
	for _, col := range []string{"source_tool", "source_cve", "verified_at"} {
		var n int
		err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('vulnerabilities') WHERE name=?", col).Scan(&n)
		if err != nil {
			t.Fatalf("check col %s: %v", col, err)
		}
		if n != 1 {
			t.Errorf("column %s not present (count=%d)", col, n)
		}
	}
}

func TestMigrateVulnerabilitiesProvenance_oldDBCompatible(t *testing.T) {
	// 模拟旧库：先建一个没有 provenance 列的 vulnerabilities 表，再跑 NewDB
	// NewDB 会 CREATE TABLE IF NOT EXISTS（不破坏已有）+ 跑迁移补列
	dbPath := filepath.Join(t.TempDir(), "old.db")
	// 先用裸 sql 建一个"旧形态"库（缺 source_tool/source_cve/verified_at，但含索引需要的列）
	// 驱动经 sqliteDriverName() 适配：CGO 构建走 mattn，-tags sqlite_pure_go 走 modernc。
	preDB, err := sql.Open(sqliteDriverName(), sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	_, err = preDB.Exec(`CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL, role_name TEXT NOT NULL DEFAULT '默认'
	)`)
	if err != nil {
		t.Fatal(err)
	}
	// 复刻真实 vulnerabilities DDL（缺 provenance 三列），供索引/迁移兼容验证
	_, err = preDB.Exec(`CREATE TABLE vulnerabilities (
		id TEXT PRIMARY KEY,
		conversation_id TEXT,
		conversation_tag TEXT,
		task_tag TEXT,
		title TEXT NOT NULL,
		description TEXT,
		severity TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		vulnerability_type TEXT,
		target TEXT,
		preconditions TEXT,
		reproduction_steps TEXT,
		evidence TEXT,
		impact TEXT,
		recommendation TEXT,
		retest_notes TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		project_id TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preDB.Exec(`INSERT INTO vulnerabilities (id, title, severity) VALUES ('v1', 'old vuln', 'high')`)
	if err != nil {
		t.Fatal(err)
	}
	preDB.Close()

	// 现在 NewDB 应该无报错地打开并补列
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB on old DB failed: %v", err)
	}
	defer db.Close()

	// 旧数据还在
	var title string
	err = db.QueryRow("SELECT title FROM vulnerabilities WHERE id='v1'").Scan(&title)
	if err != nil {
		t.Fatalf("query old row: %v", err)
	}
	if title != "old vuln" {
		t.Errorf("old data lost: title=%q", title)
	}
	// 新列存在且旧值是默认空串
	var sourceTool string
	err = db.QueryRow("SELECT COALESCE(source_tool,'NONE') FROM vulnerabilities WHERE id='v1'").Scan(&sourceTool)
	if err != nil {
		t.Fatalf("query new col: %v", err)
	}
	if sourceTool != "" {
		t.Errorf("old row source_tool should default empty, got %q", sourceTool)
	}
}

func TestGetProvenance_fromProjectFact(t *testing.T) {
	// 有漏洞关联
	f := &ProjectFact{
		RelatedVulnerabilityID: "vuln-1",
		SourceConversationID:   "conv-1",
		SourceMessageID:        "msg-1",
		Confidence:             "confirmed",
	}
	p := GetProvenance(f)
	if p.SourceType != "session+vuln" {
		t.Errorf("type=%q want session+vuln", p.SourceType)
	}
	if p.RelatedVulnerabilityID != "vuln-1" {
		t.Errorf("vuln id lost")
	}

	// 仅有会话
	f2 := &ProjectFact{SourceConversationID: "conv-2"}
	p2 := GetProvenance(f2)
	if p2.SourceType != "session" {
		t.Errorf("type=%q want session", p2.SourceType)
	}

	// 都无 → manual
	f3 := &ProjectFact{}
	p3 := GetProvenance(f3)
	if p3.SourceType != "manual" {
		t.Errorf("type=%q want manual", p3.SourceType)
	}

	// nil
	if got := GetProvenance(nil); got.SourceType != "unknown" {
		t.Errorf("nil fact type=%q want unknown", got.SourceType)
	}
}

func TestGetVulnerabilityProvenance_withCVE(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vuln.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 插入漏洞（CreateVulnerability 不会写 source_cve，直接 SQL 塞）
	v := &Vulnerability{
		Title:    "SQLi",
		Severity: "high",
		Status:   "open",
	}
	v, err = db.CreateVulnerability(v)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE vulnerabilities SET source_cve='CVE-2026-1234', source_tool='nmap', verified_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), v.ID)
	if err != nil {
		t.Fatal(err)
	}

	p, err := db.GetVulnerabilityProvenance(v.ID)
	if err != nil {
		t.Fatalf("GetVulnerabilityProvenance: %v", err)
	}
	// CVE 优先 → cve+tool 复合类型
	if p.SourceType != "cve+tool" {
		t.Errorf("type=%q want cve+tool", p.SourceType)
	}
	if p.SourceRef != "CVE-2026-1234" {
		t.Errorf("ref=%q want CVE-2026-1234", p.SourceRef)
	}
}

func TestGetVulnerabilityProvenance_toolOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vuln2.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v, err := db.CreateVulnerability(&Vulnerability{Title: "XSS", Severity: "medium", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE vulnerabilities SET source_tool='ffuf' WHERE id=?`, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.GetVulnerabilityProvenance(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceType != "tool" {
		t.Errorf("type=%q want tool", p.SourceType)
	}
	if p.SourceRef != "ffuf" {
		t.Errorf("ref=%q want ffuf", p.SourceRef)
	}
}

func TestGetVulnerabilityProvenance_notFound(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vuln3.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.GetVulnerabilityProvenance("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent vuln")
	}
}
