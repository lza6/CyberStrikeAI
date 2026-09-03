package database

import (
	"database/sql"
	"fmt"
)

// ============================================================================
// agentmemory 迁移项 4：每条记忆 provenance 引用
//
// 设计来源：agentmemory-main README "Citation provenance — JIT verification
// traces any memory back to source observations and sessions"。
//
// 主项目落地：
//   project_facts 已有 source_conversation_id/source_message_id/related_vulnerability_id
//   → provenance 部分到位，缺口是缺少「工具来源」「CVE 来源」。
//
// 本文件：
//   1. Provenance struct 统一访问
//   2. GetProvenance 从 project_facts 组装
//   3. GetVulnerabilityProvenance 从 vulnerabilities 组装（依赖 migrate_provenance 加列）
//   4. MigrateVulnerabilitiesProvenance 幂等加列 source_tool/source_cve/verified_at
//
// 兼容性（对齐 4.6 规则）：
//   - 仅 ALTER TABLE ADD COLUMN，无破坏性 DROP
//   - 每列 pragma_table_info 检查，已存在则跳过（幂等）
//   - 错误返回但不 panic，由调用方决定是否阻断启动
// ============================================================================

// Provenance 记忆来源追溯。
type Provenance struct {
	// SourceType 来源类型：tool | session | cve | manual | unknown
	SourceType string `json:"source_type"`
	// SourceRef 来源引用（工具名 / CVE-ID / 会话ID）
	SourceRef string `json:"source_ref,omitempty"`
	// SourceConversationID 来源对话
	SourceConversationID string `json:"source_conversation_id,omitempty"`
	// SourceMessageID 来源消息
	SourceMessageID string `json:"source_message_id,omitempty"`
	// RelatedVulnerabilityID 关联漏洞 ID
	RelatedVulnerabilityID string `json:"related_vulnerability_id,omitempty"`
	// VerifiedAt 人工确认时间（空表示未确认）
	VerifiedAt string `json:"verified_at,omitempty"`
	// Confidence 置信度：confirmed | tentative | deprecated
	Confidence string `json:"confidence,omitempty"`
}

// GetProvenance 从 project_facts 组装来源。
func GetProvenance(f *ProjectFact) Provenance {
	if f == nil {
		return Provenance{SourceType: "unknown"}
	}
	p := Provenance{
		SourceConversationID:   f.SourceConversationID,
		SourceMessageID:        f.SourceMessageID,
		RelatedVulnerabilityID: f.RelatedVulnerabilityID,
		Confidence:             f.Confidence,
	}
	// 来源类型推断：有关联漏洞→session+cve 复合；仅有会话→session；都无→manual
	switch {
	case f.RelatedVulnerabilityID != "":
		p.SourceType = "session+vuln"
	case f.SourceConversationID != "":
		p.SourceType = "session"
	default:
		p.SourceType = "manual"
	}
	return p
}

// GetVulnerabilityProvenance 从 vulnerabilities 组装来源。
// 依赖 migrate_provenance 加的 source_tool/source_cve/verified_at 列；
// 若列尚未迁移（旧库），用 COALESCE 返回空串，不报错。
func (db *DB) GetVulnerabilityProvenance(vulnID string) (Provenance, error) {
	if db == nil {
		return Provenance{}, fmt.Errorf("db is nil")
	}
	var (
		sourceTool, sourceCVE, conversationID, projectID, verifiedAt, status string
	)
	err := db.QueryRow(
		`SELECT
			COALESCE(source_tool,''),
			COALESCE(source_cve,''),
			COALESCE(conversation_id,''),
			COALESCE(project_id,''),
			COALESCE(verified_at,''),
			status
		 FROM vulnerabilities WHERE id = ?`, vulnID,
	).Scan(&sourceTool, &sourceCVE, &conversationID, &projectID, &verifiedAt, &status)
	if err != nil {
		if err == sql.ErrNoRows {
			return Provenance{}, fmt.Errorf("漏洞不存在")
		}
		return Provenance{}, fmt.Errorf("获取漏洞 provenance 失败: %w", err)
	}
	p := Provenance{
		SourceConversationID:   conversationID,
		RelatedVulnerabilityID: projectID,
		VerifiedAt:             verifiedAt,
		Confidence:             status,
	}
	// 类型与引用推断：CVE 优先于 tool，tool 优先于 session
	switch {
	case sourceCVE != "":
		p.SourceType = "cve"
		p.SourceRef = sourceCVE
		if sourceTool != "" {
			p.SourceType = "cve+tool"
		}
	case sourceTool != "":
		p.SourceType = "tool"
		p.SourceRef = sourceTool
	case conversationID != "":
		p.SourceType = "session"
	default:
		p.SourceType = "manual"
	}
	return p, nil
}

// migrateProvenanceColumns 幂等加列 vulnerabilities.source_tool / source_cve / verified_at。
// 兼容性：pragma_table_info 检查，已存在则跳过；ADD COLUMN NOT NULL DEFAULT ”。
func (db *DB) migrateProvenanceColumns() error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	for _, col := range []struct {
		name string
		stmt string
	}{
		{"source_tool", `ALTER TABLE vulnerabilities ADD COLUMN source_tool TEXT NOT NULL DEFAULT ''`},
		{"source_cve", `ALTER TABLE vulnerabilities ADD COLUMN source_cve TEXT NOT NULL DEFAULT ''`},
		{"verified_at", `ALTER TABLE vulnerabilities ADD COLUMN verified_at DATETIME`},
	} {
		if err := db.addColumnIfMissing("vulnerabilities", col.name, col.stmt); err != nil {
			return fmt.Errorf("添加 vulnerabilities.%s 失败: %w", col.name, err)
		}
	}
	return nil
}

// MigrateVulnerabilitiesProvenance 对外幂等入口（供 initTables 调用）。
func (db *DB) MigrateVulnerabilitiesProvenance() error {
	return db.migrateProvenanceColumns()
}
