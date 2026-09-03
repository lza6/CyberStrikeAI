package database

import (
	"math"
	"testing"
	"time"
)

// 纯函数单测：可在 CGO_ENABLED=0 下运行（不依赖 database/sql）。

func TestBaseSalienceForCategory(t *testing.T) {
	cases := map[string]float64{
		"target":     0.9,
		"finding":    0.85,
		"note":       0.5,
		"unknown":    0.5,
		"":           0.5,
		"procedure":  0.8,
		"credential": 0.9,
	}
	for cat, want := range cases {
		if got := baseSalienceForCategory(cat); got != want {
			t.Errorf("baseSalienceForCategory(%q)=%v want %v", cat, got, want)
		}
	}
}

func TestComputeSalience(t *testing.T) {
	// confirmed + 0 access → base
	got := ComputeSalience("target", 0, "confirmed")
	if math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("target/confirmed/0access: got %v want 0.9", got)
	}
	// tentative note → max(0.5,0.6)=0.6
	got = ComputeSalience("note", 0, "tentative")
	if math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("note/tentative: got %v want 0.6", got)
	}
	// deprecated → 0
	got = ComputeSalience("target", 100, "deprecated")
	if got != 0 {
		t.Fatalf("deprecated: got %v want 0", got)
	}
	// access 加成 10 次 → +0.2 上限
	got = ComputeSalience("target", 10, "confirmed")
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("10 access: got %v want 1.0 (cap)", got)
	}
	// access 加成 5 次 → +0.1
	got = ComputeSalience("target", 5, "confirmed")
	if math.Abs(got-1.0) > 1e-9 { // 0.9+0.1=1.0 capped
		t.Fatalf("5 access: got %v want 1.0", got)
	}
	// access 加成 2 次 → +0.04
	got = ComputeSalience("note", 2, "confirmed")
	if math.Abs(got-0.54) > 1e-9 {
		t.Fatalf("note 2 access: got %v want 0.54", got)
	}
}

func TestComputeRetention_temporalDecay(t *testing.T) {
	cfg := DefaultDecayConfig()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.AddDate(0, 0, 100) // 100 天后
	// salience=1.0, no access → exp(-0.01*100)=exp(-1)≈0.3679
	got := ComputeRetention(1.0, base, nil, now, cfg)
	want := math.Exp(-1.0)
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("100d decay: got %v want %v", got, want)
	}
	// 0 天衰减 → salience
	got = ComputeRetention(0.9, now, nil, now, cfg)
	if math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("0d decay: got %v want 0.9", got)
	}
}

func TestComputeRetention_reinforcementBoost(t *testing.T) {
	cfg := DefaultDecayConfig()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.AddDate(0, 0, 10) // 10 天后
	// 1 次访问在 5 天前 → +σ·(1/5)=0.3*0.2=0.06
	access := []time.Time{base.AddDate(0, 0, 5)}
	salience := 0.5
	decay := math.Exp(-0.01 * 10) // exp(-0.1)≈0.9048
	want := math.Min(1.0, salience*decay+0.06)
	got := ComputeRetention(salience, base, access, now, cfg)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("reinforcement: got %v want %v", got, want)
	}
}

func TestComputeRetention_futureAccessIgnored(t *testing.T) {
	cfg := DefaultDecayConfig()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.AddDate(0, 0, 10)
	future := base.AddDate(0, 0, 20) // 未来访问，应跳过
	got := ComputeRetention(0.9, base, []time.Time{future}, now, cfg)
	decay := math.Exp(-0.01 * 10)
	want := clamp01(0.9 * decay) // 无加成
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("future access: got %v want %v", got, want)
	}
}

func TestComputeRetention_futureCreatedAt(t *testing.T) {
	cfg := DefaultDecayConfig()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 0, 5) // 创建在未来
	// 时钟回拨保护：deltaT 按 0 处理 → temporalDecay=1.0 → score=salience
	got := ComputeRetention(0.8, future, nil, now, cfg)
	if math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("future createdAt: got %v want 0.8", got)
	}
}

func TestComputeRetention_zeroCreatedAt(t *testing.T) {
	cfg := DefaultDecayConfig()
	now := time.Now()
	// 零创建时间 → 无法计算，回退 salience
	got := ComputeRetention(0.7, time.Time{}, nil, now, cfg)
	if math.Abs(got-0.7) > 1e-9 {
		t.Fatalf("zero createdAt: got %v want 0.7", got)
	}
}

func TestComputeRetention_capsAtOne(t *testing.T) {
	cfg := DefaultDecayConfig()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.AddDate(0, 0, 1)
	// salience=1.0 + 多次近期访问 → 应被 clamp 到 1.0
	access := []time.Time{
		base.AddDate(0, 0, 0),
		base.AddDate(0, 0, 0).Add(1 * time.Hour),
		base.AddDate(0, 0, 0).Add(2 * time.Hour),
	}
	got := ComputeRetention(1.0, base, access, now, cfg)
	if got > 1.0 {
		t.Fatalf("not capped: got %v want <=1.0", got)
	}
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("expected capped to 1.0, got %v", got)
	}
}

func TestTierOf(t *testing.T) {
	cfg := DefaultDecayConfig()
	cases := []struct {
		score float64
		want  MemoryTier
	}{
		{0.75, TierHot},
		{0.7, TierHot},
		{0.69, TierWarm},
		{0.4, TierWarm},
		{0.39, TierCold},
		{0.15, TierCold},
		{0.14, TierEvictable},
		{0.0, TierEvictable},
	}
	for _, c := range cases {
		if got := TierOf(c.score, cfg); got != c.want {
			t.Errorf("TierOf(%v)=%v want %v", c.score, got, c.want)
		}
	}
}

func TestScoreProjectFact(t *testing.T) {
	cfg := DefaultDecayConfig()
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	f := &ProjectFact{
		ID:         "fact-1",
		Category:   "target",
		Confidence: "confirmed",
		CreatedAt:  now.AddDate(0, 0, -30), // 30 天前
	}
	access := []time.Time{now.AddDate(0, 0, -5)} // 5 天前访问过
	rs := ScoreProjectFact(f, access, 1, now, cfg)
	if rs == nil {
		t.Fatal("nil score")
	}
	if rs.MemoryID != "fact-1" {
		t.Errorf("memory id=%q", rs.MemoryID)
	}
	if rs.Salience != 0.9+0.02 {
		t.Errorf("salience=%v want %v", rs.Salience, 0.9+0.02)
	}
	if rs.AccessCount != 1 {
		t.Errorf("access count=%d want 1", rs.AccessCount)
	}
	if rs.Tier == TierEvictable {
		t.Errorf("30d confirmed target should not be evictable, got %v (score=%v)", rs.Tier, rs.Score)
	}
	// lastAccessed 应是 access 中最大值
	if !rs.LastAccessed.Equal(access[0]) {
		t.Errorf("last accessed=%v want %v", rs.LastAccessed, access[0])
	}
}

func TestScoreProjectFact_nil(t *testing.T) {
	if got := ScoreProjectFact(nil, nil, 0, time.Now(), DefaultDecayConfig()); got != nil {
		t.Errorf("nil fact should give nil score")
	}
}

func TestNormalizeConfidence(t *testing.T) {
	cases := map[string]string{
		"confirmed":  "confirmed",
		"deprecated": "deprecated",
		"tentative":  "tentative",
		"":           "tentative",
		"unknown":    "tentative",
	}
	for in, want := range cases {
		if got := normalizeConfidence(in); got != want {
			t.Errorf("normalizeConfidence(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClamp01(t *testing.T) {
	if got := clamp01(-0.5); got != 0 {
		t.Errorf("clamp01(-0.5)=%v want 0", got)
	}
	if got := clamp01(1.5); got != 1 {
		t.Errorf("clamp01(1.5)=%v want 1", got)
	}
	if got := clamp01(0.5); got != 0.5 {
		t.Errorf("clamp01(0.5)=%v want 0.5", got)
	}
}

func TestDefaultDecayConfig(t *testing.T) {
	cfg := DefaultDecayConfig()
	if cfg.Lambda != 0.01 {
		t.Errorf("lambda=%v want 0.01", cfg.Lambda)
	}
	if cfg.Sigma != 0.3 {
		t.Errorf("sigma=%v want 0.3", cfg.Sigma)
	}
	if cfg.TierThresholds.Hot != 0.7 || cfg.TierThresholds.Warm != 0.4 || cfg.TierThresholds.Cold != 0.15 {
		t.Errorf("thresholds=%+v want hot=0.7/warm=0.4/cold=0.15", cfg.TierThresholds)
	}
}
