package memory

import (
	"math"
	"testing"
)

func TestBM25ParamsForQuery_AdaptsToTermCount(t *testing.T) {
	cases := []struct {
		terms    int
		midpoint float64
	}{
		{1, 5.0}, {3, 5.0}, {4, 7.0}, {6, 7.0}, {7, 9.0}, {10, 10.0}, {20, 12.0},
	}
	for _, c := range cases {
		got := bm25ParamsForQuery(c.terms)
		if got.midpoint != c.midpoint {
			t.Errorf("terms=%d midpoint=%v want %v", c.terms, got.midpoint, c.midpoint)
		}
	}
}

func TestNormalizeBM25_SigmoidBounds(t *testing.T) {
	p := bm25Params{midpoint: 7.0, steepness: 0.6}
	// At midpoint, output is exactly 0.5 (the sigmoid center).
	if got := normalizeBM25(7.0, p); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("at midpoint expected 0.5, got %v", got)
	}
	// Below midpoint → < 0.5; above → > 0.5.
	if normalizeBM25(3.0, p) >= 0.5 {
		t.Fatal("below midpoint must be < 0.5")
	}
	if normalizeBM25(12.0, p) <= 0.5 {
		t.Fatal("above midpoint must be > 0.5")
	}
	// Monotonic: higher raw → higher normalized (within non-extreme range).
	a := normalizeBM25(5.0, p)
	b := normalizeBM25(9.0, p)
	if a >= b {
		t.Fatalf("sigmoid must be monotonic: %v >= %v", a, b)
	}
	// Extremes clamp to asymptotes (no NaN).
	if got := normalizeBM25(1e6, p); got != 1.0 {
		t.Fatalf("huge raw must clamp to 1.0, got %v", got)
	}
	if got := normalizeBM25(-1e6, p); got != 0.0 {
		t.Fatalf("huge negative must clamp to 0.0, got %v", got)
	}
}

func TestMaxPossibleDivisor_SignalCombinations(t *testing.T) {
	if g := maxPossibleDivisor(false, false); g != 1.0 {
		t.Errorf("sem only = %v, want 1.0", g)
	}
	if g := maxPossibleDivisor(true, false); g != 2.0 {
		t.Errorf("sem+bm25 = %v, want 2.0", g)
	}
	if g := maxPossibleDivisor(false, true); g != 1.5 {
		t.Errorf("sem+entity = %v, want 1.5", g)
	}
	if g := maxPossibleDivisor(true, true); g != 2.5 {
		t.Errorf("sem+bm25+entity = %v, want 2.5", g)
	}
}

func TestScoreAndRank_SemanticOnlyRescales(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "a"}, SemanticScore: 0.9},
		{Instance: &FactInstance{ID: "b"}, SemanticScore: 0.4},
	}
	res := ScoreAndRank(cands, "query", ScoreAndRankOptions{TopK: 10})
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
	if res[0].Instance.ID != "a" {
		t.Errorf("best first = %q, want a", res[0].Instance.ID)
	}
	// Semantic-only: score = semantic / 1.0 = semantic.
	if math.Abs(res[0].CombinedScore-0.9) > 1e-9 {
		t.Errorf("score = %v, want 0.9", res[0].CombinedScore)
	}
}

func TestScoreAndRank_ThresholdGateExcludesLowSemantic(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "low"}, SemanticScore: 0.2, BM25RawScore: 20.0}, // strong BM25 but below threshold
		{Instance: &FactInstance{ID: "ok"}, SemanticScore: 0.6, BM25RawScore: 0.0},
	}
	res := ScoreAndRank(cands, "query", ScoreAndRankOptions{Threshold: 0.5, TopK: 10})
	// "low" must be excluded even though it has the strongest BM25 — threshold gate.
	if len(res) != 1 || res[0].Instance.ID != "ok" {
		t.Fatalf("threshold gate must exclude low; got %+v", res)
	}
}

func TestScoreAndRank_ThreeSignalFusionWeights(t *testing.T) {
	// All three signals at saturation: semantic=1, bm25 raw→sigmoid≈1, entity=0.5.
	cands := []FusionCandidate{
		{
			Instance:     &FactInstance{ID: "full"},
			SemanticScore: 1.0,
			BM25RawScore:  20.0, // well above any midpoint → sigmoid ≈ 1
			EntityBoost:   0.5,
		},
	}
	res := ScoreAndRank(cands, "the quick brown fox", ScoreAndRankOptions{})
	if len(res) != 1 {
		t.Fatalf("want 1, got %d", len(res))
	}
	// raw_combined = 1 + ~1 + 0.5 = ~2.5; maxPossible = 2.5 → ~1.0
	if res[0].CombinedScore < 0.95 {
		t.Errorf("saturated three-signal score = %v, want ~1.0", res[0].CombinedScore)
	}
}

func TestScoreAndRank_EntityBoostCappedAtWeight(t *testing.T) {
	// Entity boost above the cap (0.5) must be clamped.
	cands := []FusionCandidate{
		{
			Instance:     &FactInstance{ID: "x"},
			SemanticScore: 0.5,
			BM25RawScore:  0.0,
			EntityBoost:   1.0, // over the cap
		},
	}
	res := ScoreAndRank(cands, "q", ScoreAndRankOptions{Explain: true})
	if res[0].Explanation.EntityBoost > EntityBoostWeight {
		t.Errorf("entity boost = %v, must be capped at %v", res[0].Explanation.EntityBoost, EntityBoostWeight)
	}
}

func TestScoreAndRank_TopKTruncation(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "1"}, SemanticScore: 0.1},
		{Instance: &FactInstance{ID: "2"}, SemanticScore: 0.2},
		{Instance: &FactInstance{ID: "3"}, SemanticScore: 0.3},
	}
	res := ScoreAndRank(cands, "q", ScoreAndRankOptions{TopK: 2})
	if len(res) != 2 {
		t.Fatalf("TopK=2 must truncate to 2, got %d", len(res))
	}
	if res[0].Instance.ID != "3" {
		t.Errorf("best first = %q, want 3", res[0].Instance.ID)
	}
}

func TestScoreAndRank_TiebreakActiveOverSuperseded(t *testing.T) {
	cands := []FusionCandidate{
		{Instance: &FactInstance{ID: "old", LifecycleState: LifecycleSuperseded, Version: 1}, SemanticScore: 0.7},
		{Instance: &FactInstance{ID: "new", LifecycleState: LifecycleActive, Version: 2}, SemanticScore: 0.7},
	}
	res := ScoreAndRank(cands, "q", ScoreAndRankOptions{})
	if res[0].Instance.ID != "new" {
		t.Errorf("tie must go to active, got %q first", res[0].Instance.ID)
	}
}

func TestComputeEntityBoosts_DedupAndCap(t *testing.T) {
	// Two query entities both hitting the same instance; boost must take the max
	// similarity (bestSim) and apply memory_count_weight damping.
	lookup := func(entity string) []EntityHit {
		switch entity {
		case "Tesla":
			return []EntityHit{{InstanceID: "m1", Similarity: 0.9}}
		case "Model3":
			return []EntityHit{{InstanceID: "m1", Similarity: 0.7}}
		default:
			return nil
		}
	}
	boosts := ComputeEntityBoosts([]string{"Tesla", "Model3"}, lookup)
	b, ok := boosts["m1"]
	if !ok {
		t.Fatal("m1 must be boosted")
	}
	// memory_count_weight = 1/(1+0.001*(2-1)^2) = 1/1.001 ≈ 0.999
	// boost = 0.9 * 0.5 * 0.999 ≈ 0.4496
	want := 0.9 * 0.5 * (1.0 / 1.001)
	if math.Abs(b-want) > 1e-6 {
		t.Errorf("boost = %v, want %v", b, want)
	}
}

func TestComputeEntityBoosts_SimilarityFloor(t *testing.T) {
	lookup := func(entity string) []EntityHit {
		return []EntityHit{{InstanceID: "m2", Similarity: 0.3}} // below 0.5 floor
	}
	boosts := ComputeEntityBoosts([]string{"low"}, lookup)
	if len(boosts) != 0 {
		t.Errorf("below-floor similarity must produce no boost, got %v", boosts)
	}
}

func TestComputeEntityBoosts_EntityCap8(t *testing.T) {
	// 10 entities — only first 8 should be queried.
	calls := 0
	lookup := func(entity string) []EntityHit {
		calls++
		return []EntityHit{{InstanceID: "x" + entity, Similarity: 0.9}}
	}
	_ = ComputeEntityBoosts([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, lookup)
	if calls != 8 {
		t.Errorf("entity cap = %d calls, want 8", calls)
	}
}
