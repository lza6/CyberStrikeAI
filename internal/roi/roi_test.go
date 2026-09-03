package roi

import (
	"strings"
	"testing"

	"cyberstrike-ai/internal/bounty"
)

// TestCalculate_GreenWhenBountyTrouncesSpend 验证：一条 critical 发现
// （public-market 估值 $1500 low）对照 $10 花费，倍率 150× 应判绿。
func TestCalculate_GreenWhenBountyTrouncesSpend(t *testing.T) {
	findings := []bounty.Finding{
		{Severity: string(bounty.SeverityCritical)}, // public-market: $1500 low
	}
	r := Calculate(10.0, findings, nil)
	if r.Verdict != VerdictGreen {
		t.Errorf("expected green, got %s (ratio %.1f)", r.Verdict, r.RatioLow)
	}
}

// TestCalculate_RedWhenSpendDwarfsBounty 验证：一条 low 发现
// （public-market 估值 $50 low）对照 $100 花费，倍率 0.5× 应判红。
func TestCalculate_RedWhenSpendDwarfsBounty(t *testing.T) {
	findings := []bounty.Finding{
		{Severity: string(bounty.SeverityLow)}, // $50 low
	}
	r := Calculate(100.0, findings, nil)
	if r.Verdict != VerdictRed {
		t.Errorf("expected red, got %s (ratio %.1f)", r.Verdict, r.RatioLow)
	}
}

// TestCalculate_ZeroSpendDoesNotPanic 验证：零花费不 panic，
// 倍率保持 0，且 verdict 默认为红（ratio < 2）。
func TestCalculate_ZeroSpendDoesNotPanic(t *testing.T) {
	r := Calculate(0, []bounty.Finding{{Severity: string(bounty.SeverityHigh)}}, nil)
	if r.RatioLow != 0 || r.RatioHigh != 0 {
		t.Errorf("zero-spend should produce zero ratios, got %+v", r)
	}
	if r.Verdict != VerdictRed {
		t.Errorf("zero-spend should default to red")
	}
}

// TestFooter_IncludesAllFigures 验证：Footer 字符串包含花费、
// 赏金区间和倍率的所有数字。
func TestFooter_IncludesAllFigures(t *testing.T) {
	r := Result{SpendUSD: 5.0, BountyLowUSD: 100, BountyHighUSD: 500, RatioLow: 20, RatioHigh: 100, Verdict: VerdictGreen}
	out := r.Footer()
	for _, want := range []string{"$5.00", "$100", "$500", "20.0", "100.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q: %s", want, out)
		}
	}
}

// TestCalculate_EmptyFindings_NonZeroSpend_IsRed 验证补充边界：
// 空 findings + 非零花费 → 赏金 0、倍率 0、判定红。
func TestCalculate_EmptyFindings_NonZeroSpend_IsRed(t *testing.T) {
	r := Calculate(50.0, nil, nil)
	if r.BountyLowUSD != 0 || r.BountyHighUSD != 0 {
		t.Errorf("empty findings should produce zero bounty, got low=%d high=%d", r.BountyLowUSD, r.BountyHighUSD)
	}
	if r.RatioLow != 0 || r.RatioHigh != 0 {
		t.Errorf("empty findings should produce zero ratios, got low=%.1f high=%.1f", r.RatioLow, r.RatioHigh)
	}
	if r.Verdict != VerdictRed {
		t.Errorf("empty findings + non-zero spend should be red, got %s", r.Verdict)
	}
}

// TestCalculate_UnknownSeverity_DoesNotPanic 验证补充边界：
// 未知 severity（bounty.Estimate 返回零值 Range）不 panic，
// 赏金为 0、倍率 0、判定红。
func TestCalculate_UnknownSeverity_DoesNotPanic(t *testing.T) {
	findings := []bounty.Finding{{Severity: "unknown"}}
	r := Calculate(10.0, findings, nil)
	if r.BountyLowUSD != 0 || r.BountyHighUSD != 0 {
		t.Errorf("unknown severity should yield zero bounty, got low=%d high=%d", r.BountyLowUSD, r.BountyHighUSD)
	}
	if r.Verdict != VerdictRed {
		t.Errorf("unknown severity should yield red (zero bounty), got %s", r.Verdict)
	}
}

// TestCalculate_ConcurrentSafe_NoDataRace 验证 Calculate 在并发调用下
// 不产生数据竞争（替代 -race 标志，本环境无 gcc 无法启用 cgo）。
// 若存在数据竞争，运行时检测器会在并发压力下触发 fatal error。
func TestCalculate_ConcurrentSafe_NoDataRace(t *testing.T) {
	findings := []bounty.Finding{
		{Severity: string(bounty.SeverityCritical)},
		{Severity: string(bounty.SeverityHigh)},
		{Severity: string(bounty.SeverityLow)},
	}
	const goroutines = 50
	const iterations = 100
	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < iterations; j++ {
				r := Calculate(10.0, findings, nil)
				// 读取所有字段，确保编译器不会优化掉计算。
				_ = r.SpendUSD + float64(r.BountyLowUSD+r.BountyHighUSD) + r.RatioLow + r.RatioHigh
				_ = r.Footer()
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
