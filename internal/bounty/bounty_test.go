package bounty

import "testing"

// TestEstimate_FallsBackToPublicMarket 验证：stats 为 nil 时，
// Estimate 回退到 publicMarket 表，且 critical 档数值符合保守下取整。
func TestEstimate_FallsBackToPublicMarket(t *testing.T) {
	f := Finding{Severity: string(SeverityCritical)}
	got := Estimate(f, nil)
	// publicMarket[critical] = {1500, 10000, "industry-average"}
	if got.LowUSD != 1500 || got.HighUSD != 10000 {
		t.Errorf("public-market range invalid: %+v", got)
	}
	if got.LowUSD <= 0 || got.HighUSD <= got.LowUSD {
		t.Errorf("public-market range non-positive or non-increasing: %+v", got)
	}
	if got.Source != "industry-average" {
		t.Errorf("expected industry-average source, got %q", got.Source)
	}
}

// TestEstimate_PrefersProgramStats 验证：stats 同时提供 avg 与 top 时，
// 项目数据优先于 publicMarket，且 Source 标记为 "program:<slug>"。
func TestEstimate_PrefersProgramStats(t *testing.T) {
	stats := &ProgramStats{
		Slug:               "shopify",
		AveragePerSeverity: map[string]int{string(SeverityHigh): 1500},
		TopPerSeverity:     map[string]int{string(SeverityHigh): 5000},
	}
	got := Estimate(Finding{Severity: string(SeverityHigh)}, stats)
	if got.LowUSD != 1500 || got.HighUSD != 5000 {
		t.Errorf("program stats not honoured: %+v", got)
	}
	if got.Source != "program:shopify" {
		t.Errorf("source not tagged with program: %q", got.Source)
	}
}

// TestEstimate_OnlyAverage_DerivesTop 验证：stats 只提供 avg 时，
// high 按 avg*5/2 推导（avg=400 → high=1000）。
func TestEstimate_OnlyAverage_DerivesTop(t *testing.T) {
	stats := &ProgramStats{
		Slug:               "x",
		AveragePerSeverity: map[string]int{string(SeverityMedium): 400},
	}
	got := Estimate(Finding{Severity: string(SeverityMedium)}, stats)
	if got.LowUSD != 400 || got.HighUSD != 1000 {
		t.Errorf("derivation off: %+v", got)
	}
}

// TestEstimate_OnlyTop_DerivesLow 验证：stats 只提供 top 时，
// low 按 top/2 推导（top=5000 → low=2500）。
func TestEstimate_OnlyTop_DerivesLow(t *testing.T) {
	stats := &ProgramStats{
		Slug:           "y",
		TopPerSeverity: map[string]int{string(SeverityHigh): 5000},
	}
	got := Estimate(Finding{Severity: string(SeverityHigh)}, stats)
	if got.LowUSD != 2500 || got.HighUSD != 5000 {
		t.Errorf("derivation off: %+v", got)
	}
	if got.Source != "program:y" {
		t.Errorf("source not tagged with program: %q", got.Source)
	}
}

// TestTotal_Sums 验证：Total 累加 critical+high+medium（全部回退 publicMarket）。
// critical(1500..10000) + high(500..3000) + medium(150..800)
// = low 2150, high 13800。
func TestTotal_Sums(t *testing.T) {
	findings := []Finding{
		{Severity: string(SeverityCritical)},
		{Severity: string(SeverityHigh)},
		{Severity: string(SeverityMedium)},
	}
	low, high := Total(findings, nil)
	if low != 2150 || high != 13800 {
		t.Errorf("total invalid: low=%d high=%d (want 2150/13800)", low, high)
	}
	if low <= 0 || high <= low {
		t.Errorf("total non-positive or non-increasing: low=%d high=%d", low, high)
	}
}

// TestTotal_Empty 验证：空 findings 返回 (0, 0)。
func TestTotal_Empty(t *testing.T) {
	low, high := Total(nil, nil)
	if low != 0 || high != 0 {
		t.Errorf("empty total invalid: low=%d high=%d (want 0/0)", low, high)
	}
	low2, high2 := Total([]Finding{}, &ProgramStats{Slug: "z"})
	if low2 != 0 || high2 != 0 {
		t.Errorf("empty total with stats invalid: low=%d high=%d (want 0/0)", low2, high2)
	}
}

// TestEstimate_UnknownSeverity_ZeroRange 验证：未知 severity 返回零值
// Range（LowUSD=0, HighUSD=0），且 Source="industry-average"。
func TestEstimate_UnknownSeverity_ZeroRange(t *testing.T) {
	f := Finding{Severity: "unknown-severity"}
	got := Estimate(f, nil)
	if got.LowUSD != 0 || got.HighUSD != 0 {
		t.Errorf("unknown severity should return zero range, got %+v", got)
	}
	if got.Source != "industry-average" {
		t.Errorf("unknown severity source = %q, want industry-average", got.Source)
	}
}

// TestEstimate_UnknownSeverity_WithStats_FallsBack 验证：stats 不含
// 该 severity 的任何条目时，即使 stats 非 nil，也回退到 publicMarket；
// publicMarket 没有该 severity 时返回零值。
func TestEstimate_UnknownSeverity_WithStats_FallsBack(t *testing.T) {
	stats := &ProgramStats{
		Slug:               "p",
		AveragePerSeverity: map[string]int{string(SeverityHigh): 1000},
		TopPerSeverity:     map[string]int{string(SeverityHigh): 3000},
	}
	// stats 中没有 "critical" 条目 → 回退 publicMarket
	got := Estimate(Finding{Severity: string(SeverityCritical)}, stats)
	if got.LowUSD != 1500 || got.HighUSD != 10000 {
		t.Errorf("expected public-market fallback for critical, got %+v", got)
	}
	if got.Source != "industry-average" {
		t.Errorf("expected industry-average source, got %q", got.Source)
	}
	// stats 中也没有 "unknown" 条目 → publicMarket 也没有 → 零值
	got2 := Estimate(Finding{Severity: "unknown"}, stats)
	if got2.LowUSD != 0 || got2.HighUSD != 0 {
		t.Errorf("expected zero range for unknown severity, got %+v", got2)
	}
	if got2.Source != "industry-average" {
		t.Errorf("expected industry-average source, got %q", got2.Source)
	}
}

// TestEstimate_AllPublicMarketBuckets 验证：publicMarket 全档位
// （critical/high/medium/low/info）数值符合 HackerOne 中位数向下取整的
// 保守取值，且区间单调（low < high，info 例外 low=0）。
func TestEstimate_AllPublicMarketBuckets(t *testing.T) {
	cases := []struct {
		sev       Severity
		wantLow   int
		wantHigh  int
		wantLabel string
	}{
		{SeverityCritical, 1500, 10000, "critical"},
		{SeverityHigh, 500, 3000, "high"},
		{SeverityMedium, 150, 800, "medium"},
		{SeverityLow, 50, 200, "low"},
		{SeverityInformational, 0, 50, "info"},
	}
	for _, c := range cases {
		f := Finding{Severity: string(c.sev)}
		got := Estimate(f, nil)
		if got.LowUSD != c.wantLow || got.HighUSD != c.wantHigh {
			t.Errorf("[%s] public-market mismatch: got %+v, want low=%d high=%d",
				c.wantLabel, got, c.wantLow, c.wantHigh)
		}
		if got.Source != "industry-average" {
			t.Errorf("[%s] source = %q, want industry-average", c.wantLabel, got.Source)
		}
		// info 档 low=0 合法；其余档位要求 low>0 且 high>low。
		if c.sev != SeverityInformational {
			if got.LowUSD <= 0 || got.HighUSD <= got.LowUSD {
				t.Errorf("[%s] non-positive or non-increasing range: %+v", c.wantLabel, got)
			}
		} else {
			// info 档：low=0，high 必须为正。
			if got.HighUSD <= 0 {
				t.Errorf("[%s] info high must be positive: %+v", c.wantLabel, got)
			}
		}
	}
}

// TestEstimate_StatsWithEmptyMaps 验证：stats 非 nil 但两个 map 都为空
// （或都未包含目标 severity）时，回退到 publicMarket。
func TestEstimate_StatsWithEmptyMaps(t *testing.T) {
	stats := &ProgramStats{Slug: "empty-program"}
	got := Estimate(Finding{Severity: string(SeverityHigh)}, stats)
	if got.LowUSD != 500 || got.HighUSD != 3000 {
		t.Errorf("expected public-market fallback when stats maps empty: got %+v", got)
	}
	if got.Source != "industry-average" {
		t.Errorf("expected industry-average source, got %q", got.Source)
	}
}

// TestTotal_WithProgramStats 验证：Total 在有 stats 时累加 program-derived
// 区间。两条 high（avg=1500, top=5000）→ low=3000, high=10000。
func TestTotal_WithProgramStats(t *testing.T) {
	stats := &ProgramStats{
		Slug:               "shopify",
		AveragePerSeverity: map[string]int{string(SeverityHigh): 1500},
		TopPerSeverity:     map[string]int{string(SeverityHigh): 5000},
	}
	findings := []Finding{
		{Severity: string(SeverityHigh)},
		{Severity: string(SeverityHigh)},
	}
	low, high := Total(findings, stats)
	if low != 3000 || high != 10000 {
		t.Errorf("total with stats invalid: low=%d high=%d (want 3000/10000)", low, high)
	}
}

// TestTotal_MixedSeverity_UnknownIncludesZero 验证：Total 混合已知与未知
// severity 时，未知档贡献 (0, 0)，不破坏累加。
// critical(1500..10000) + unknown(0..0) + high(500..3000)
// = low 2000, high 13000。
func TestTotal_MixedSeverity_UnknownIncludesZero(t *testing.T) {
	findings := []Finding{
		{Severity: string(SeverityCritical)},
		{Severity: "unknown"},
		{Severity: string(SeverityHigh)},
	}
	low, high := Total(findings, nil)
	if low != 2000 || high != 13000 {
		t.Errorf("mixed total invalid: low=%d high=%d (want 2000/13000)", low, high)
	}
}
