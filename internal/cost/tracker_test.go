package cost

import (
	"testing"
)

// TestTrackerAddTotal 验证累加与 Total。
func TestTrackerAddTotal(t *testing.T) {
	tr := New()
	if err := tr.Add(UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 1000, OutputTokens: 500}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := tr.Add(UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 2000, OutputTokens: 1000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	total := tr.Total()
	if total.InputTokens != 3000 {
		t.Errorf("input = %d, want 3000", total.InputTokens)
	}
	if total.OutputTokens != 1500 {
		t.Errorf("output = %d, want 1500", total.OutputTokens)
	}
	// 成本应 > 0（claude-sonnet-4 前缀匹配）
	if total.CostUSD <= 0 {
		t.Errorf("cost = %f, want > 0", total.CostUSD)
	}
}

// TestTrackerReportByModel 验证按 model 分组。
func TestTrackerReportByModel(t *testing.T) {
	tr := New()
	_ = tr.Add(UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 1000, OutputTokens: 0})
	_ = tr.Add(UsageSnapshot{Model: "gpt-4o", InputTokens: 500, OutputTokens: 0})
	_ = tr.Add(UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 2000, OutputTokens: 0})
	rep := tr.Report()
	if len(rep.ByModel) != 2 {
		t.Errorf("expected 2 models, got %d", len(rep.ByModel))
	}
	if rep.ByModel["claude-sonnet-4-5"].InputTokens != 3000 {
		t.Errorf("sonnet input = %d, want 3000", rep.ByModel["claude-sonnet-4-5"].InputTokens)
	}
	if rep.ByModel["gpt-4o"].InputTokens != 500 {
		t.Errorf("gpt-4o input = %d, want 500", rep.ByModel["gpt-4o"].InputTokens)
	}
}

// TestTrackerEmptyModel 验证空 model 报错。
func TestTrackerEmptyModel(t *testing.T) {
	tr := New()
	if err := tr.Add(UsageSnapshot{Model: ""}); err == nil {
		t.Error("expected error for empty model")
	}
}

// TestLookupPrice 验证前缀匹配。
func TestLookupPrice(t *testing.T) {
	p, ok := LookupPrice("claude-sonnet-4-5-20250929")
	if !ok {
		t.Error("expected claude-sonnet-4-5 to match")
	}
	if p.InputPer1M != 3 {
		t.Errorf("input price = %f, want 3", p.InputPer1M)
	}
	p, ok = LookupPrice("unknown-model")
	if ok {
		t.Error("expected unknown model to not match")
	}
	_, ok = LookupPrice("")
	if ok {
		t.Error("expected empty model to not match")
	}
}

// TestCalculate 验证成本计算公式。
func TestCalculate(t *testing.T) {
	// claude-sonnet-4: input=3/1M, output=15/1M
	u := UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 1000000, OutputTokens: 1000000}
	c := Calculate(u)
	// 3 + 15 = 18
	if c < 17.9 || c > 18.1 {
		t.Errorf("calculate = %f, want ~18", c)
	}
}

// TestRegisterPrice 验证注册自定义定价。
func TestRegisterPrice(t *testing.T) {
	RegisterPrice("my-custom-model", ModelPrice{InputPer1M: 1, OutputPer1M: 2})
	p, ok := LookupPrice("my-custom-model-v2")
	if !ok {
		t.Error("expected registered model to match")
	}
	if p.InputPer1M != 1 {
		t.Errorf("input = %f, want 1", p.InputPer1M)
	}
}

// TestTrackerReset 验证清零。
func TestTrackerReset(t *testing.T) {
	tr := New()
	_ = tr.Add(UsageSnapshot{Model: "claude-sonnet-4-5", InputTokens: 1000, OutputTokens: 0})
	tr.Reset()
	if tr.Total().InputTokens != 0 {
		t.Errorf("after reset, input = %d, want 0", tr.Total().InputTokens)
	}
}

// TestCacheTokens 验证 cache token 计费。
func TestCacheTokens(t *testing.T) {
	tr := New()
	// claude-sonnet-4: cache_read=0.3/1M, cache_write=3.75/1M
	_ = tr.Add(UsageSnapshot{
		Model:            "claude-sonnet-4-5",
		CacheReadTokens:  1000000,
		CacheWriteTokens: 1000000,
	})
	total := tr.Total()
	// 0.3 + 3.75 = 4.05
	if total.CostUSD < 4.0 || total.CostUSD > 4.1 {
		t.Errorf("cache cost = %f, want ~4.05", total.CostUSD)
	}
}
