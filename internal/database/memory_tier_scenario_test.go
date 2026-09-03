package database

import (
	"testing"
	"time"
)

// 迁移项 1 的集成验收：ScoreProjectFact 端到端场景（在 CGO 测试环境跑）。
// 纯函数部分已在 memory_tier_test.go 覆盖，这里补「分层分布」场景。

func TestRetentionTierDistribution(t *testing.T) {
	cfg := DefaultDecayConfig()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	// 场景：30 天前的项目。一条刚确认的 target（hot），一条 90 天前的旧 note（evictable）
	hot := &ProjectFact{
		ID: "hot-1", Category: "target", Confidence: "confirmed",
		CreatedAt: now.AddDate(0, 0, -3), // 3 天前，decay≈exp(-0.03)≈0.97
	}
	old := &ProjectFact{
		ID: "cold-1", Category: "note", Confidence: "tentative",
		CreatedAt: now.AddDate(0, 0, -120), // 120 天前，decay≈exp(-1.2)≈0.30
	}

	rsHot := ScoreProjectFact(hot, nil, 0, now, cfg)
	rsOld := ScoreProjectFact(old, nil, 0, now, cfg)

	if rsHot.Tier != TierHot {
		t.Errorf("recent confirmed target: tier=%v score=%v want hot", rsHot.Tier, rsHot.Score)
	}
	if rsOld.Tier == TierHot {
		t.Errorf("120d tentative note: tier=%v score=%v should not be hot", rsOld.Tier, rsOld.Score)
	}
	if rsHot.Score <= rsOld.Score {
		t.Errorf("hot score %v should exceed old score %v", rsHot.Score, rsOld.Score)
	}
}

func TestRetentionDeprecatedNeverReinforced(t *testing.T) {
	cfg := DefaultDecayConfig()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	dep := &ProjectFact{
		ID: "dep-1", Category: "target", Confidence: "deprecated",
		CreatedAt: now.AddDate(0, 0, -1),
	}
	// 即使大量访问，deprecated 也应得 0 分（salience=0 且无强化路径抵消归零）
	access := []time.Time{now.Add(-time.Hour), now.Add(-2 * time.Hour)}
	rs := ScoreProjectFact(dep, access, 100, now, cfg)
	if rs.Score != 0 {
		t.Errorf("deprecated with 100 accesses: score=%v want 0", rs.Score)
	}
	if rs.Tier != TierEvictable {
		t.Errorf("deprecated tier=%v want evictable", rs.Tier)
	}
}
