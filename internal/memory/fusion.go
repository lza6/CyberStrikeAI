package memory

import (
	"math"
	"sort"

	"cyberstrike-ai/internal/ctxindex"
)

// FusionCandidate is one memory instance paired with its three retrieval signals.
// The signals are filled by the retriever before ScoreAndRank combines them.
type FusionCandidate struct {
	Instance      *FactInstance
	SemanticScore float64 // cosine similarity in [0,1], 0 when below threshold
	BM25RawScore  float64 // raw Okapi BM25 score (unbounded, typically 0..20)
	EntityBoost   float64 // pre-computed entity boost in [0, 0.5]
}

// bm25 normalization parameters adapt to query length. This mirrors mem0's
// get_bm25_params (mem0/mem0/utils/scoring.py:16-40): shorter queries have a
// lower midpoint and steeper sigmoid so a single strong term still discriminates.
type bm25Params struct {
	midpoint  float64
	steepness float64
}

func bm25ParamsForQuery(termCount int) bm25Params {
	switch {
	case termCount <= 3:
		return bm25Params{midpoint: 5.0, steepness: 0.7}
	case termCount <= 6:
		return bm25Params{midpoint: 7.0, steepness: 0.6}
	case termCount <= 9:
		return bm25Params{midpoint: 9.0, steepness: 0.5}
	case termCount <= 15:
		return bm25Params{midpoint: 10.0, steepness: 0.5}
	default:
		return bm25Params{midpoint: 12.0, steepness: 0.5}
	}
}

// normalizeBM25 applies the sigmoid that compresses raw BM25 (0..20+) into [0,1].
// Formula: 1 / (1 + exp(-steepness * (raw - midpoint)))
// Mirrors mem0/mem0/utils/scoring.py:43-54.
func normalizeBM25(raw float64, p bm25Params) float64 {
	x := -p.steepness * (raw - p.midpoint)
	// guard against overflow in exp for extreme raw scores
	if x > 30 {
		return 0.0
	}
	if x < -30 {
		return 1.0
	}
	return 1.0 / (1.0 + math.Exp(x))
}

// EntityBoostWeight is the fixed contribution cap of the entity signal in the
// three-signal fusion. mem0 uses 0.5 (mem0/mem0/utils/scoring.py:57) so that
// even a maximally-relevant entity cannot dominate semantic+BM25 combined.
const EntityBoostWeight = 0.5

// maxPossibleDivisor is the adaptive divisor controlling each signal's weight.
// When fewer signals are present, the divisor shrinks so the combined score
// stays normalized to [0,1]:
//
//	semantic only            → 1.0  (semantic gets full weight)
//	semantic + BM25           → 2.0  (50% / 50%)
//	semantic + entity         → 1.5  (66% / 33%)
//	semantic + BM25 + entity  → 2.5  (40% / 40% / 20%)
//
// Mirrors mem0/mem0/utils/scoring.py:97-101.
func maxPossibleDivisor(hasBM25, hasEntity bool) float64 {
	switch {
	case hasBM25 && hasEntity:
		return 2.5
	case hasBM25:
		return 2.0
	case hasEntity:
		return 1.5
	default:
		return 1.0
	}
}

// FusionResult is the scored output of ScoreAndRank.
type FusionResult struct {
	Instance      *FactInstance
	CombinedScore float64 // normalized to [0,1]
	Explanation   FusionExplanation
}

// FusionExplanation breaks down the combined score for debugging and evals.
// It is populated when Explain is true; otherwise zero-valued.
type FusionExplanation struct {
	SemanticNorm           float64
	BM25Norm               float64
	EntityBoost            float64
	MaxPossible            float64
	RawCombined            float64
	SemanticBelowThreshold bool
	// Temporal fields are filled by ApplyTemporalAdjustment.
	TemporalDelta  float64
	TemporalReason string
}

// ScoreAndRankOptions configures the fusion scorer.
type ScoreAndRankOptions struct {
	// Threshold is the minimum semantic score for a candidate to remain in the
	// pool. Candidates below it are excluded before BM25/entity can rescue
	// them — mirroring mem0's threshold gate (scoring.py:110-112).
	Threshold float64
	// TopK caps the returned results; 0 means no cap.
	TopK int
	// Explain requests per-signal breakdown on results.
	Explain bool
}

// ScoreAndRank fuses semantic, BM25, and entity signals into a single [0,1]
// score per candidate and returns them sorted best-first.
//
// The algorithm is a direct port of mem0/mem0/utils/scoring.py:60-139:
//
//  1. Drop candidates whose SemanticScore < Threshold (threshold gate).
//  2. Normalize BM25 via sigmoid (query-length-adaptive midpoint/steepness).
//  3. Cap entity boost at EntityBoostWeight (0.5).
//  4. combined = (semantic + bm25Norm + entityBoost) / maxPossible
//  5. Sort descending; truncate to TopK.
//
// Query is the original user query (used only for BM25 term-count adaptation);
// the BM25 raw scores are assumed pre-computed by the caller via
// ctxindex.BM25Scores (the pure-Go Okapi implementation we already ship).
func ScoreAndRank(candidates []FusionCandidate, query string, opts ScoreAndRankOptions) []FusionResult {
	if len(candidates) == 0 {
		return nil
	}
	// Query-length adaptation for BM25 sigmoid parameters.
	termCount := len(ctxindex.Tokenize(query))
	params := bm25ParamsForQuery(termCount)

	hasBM25 := false
	hasEntity := false
	for _, c := range candidates {
		if c.BM25RawScore > 0 {
			hasBM25 = true
		}
		if c.EntityBoost > 0 {
			hasEntity = true
		}
	}
	maxPossible := maxPossibleDivisor(hasBM25, hasEntity)

	results := make([]FusionResult, 0, len(candidates))
	for _, c := range candidates {
		exp := FusionExplanation{MaxPossible: maxPossible}

		// Threshold gate: a candidate below the semantic floor is excluded
		// entirely — BM25/entity cannot rescue it.
		if opts.Threshold > 0 && c.SemanticScore < opts.Threshold {
			exp.SemanticBelowThreshold = true
			if opts.Explain {
				results = append(results, FusionResult{
					Instance:      c.Instance,
					CombinedScore: 0,
					Explanation:   exp,
				})
			}
			continue
		}

		bm25Norm := 0.0
		if hasBM25 && c.BM25RawScore > 0 {
			bm25Norm = normalizeBM25(c.BM25RawScore, params)
		}
		entityBoost := 0.0
		if hasEntity {
			entityBoost = math.Min(c.EntityBoost, EntityBoostWeight)
		}

		rawCombined := c.SemanticScore + bm25Norm + entityBoost
		combined := rawCombined / maxPossible
		if combined > 1.0 {
			combined = 1.0
		}

		exp.SemanticNorm = c.SemanticScore
		exp.BM25Norm = bm25Norm
		exp.EntityBoost = entityBoost
		exp.RawCombined = rawCombined

		results = append(results, FusionResult{
			Instance:      c.Instance,
			CombinedScore: combined,
			Explanation:   exp,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].CombinedScore != results[j].CombinedScore {
			return results[i].CombinedScore > results[j].CombinedScore
		}
		// Stable tiebreak: prefer active lifecycle, then earlier version
		// (more recent truth wins ties), then ID for determinism.
		ri, rj := results[i].Instance, results[j].Instance
		if ri.LifecycleState != rj.LifecycleState {
			return lifecycleRank(ri.LifecycleState) < lifecycleRank(rj.LifecycleState)
		}
		if ri.Version != rj.Version {
			return ri.Version > rj.Version
		}
		return ri.ID < rj.ID
	})

	if opts.TopK > 0 && len(results) > opts.TopK {
		results = results[:opts.TopK]
	}
	return results
}

// lifecycleRank orders lifecycle states so active wins ties over merged over
// superseded (i.e., current truth is preferred when scores are equal).
func lifecycleRank(s LifecycleState) int {
	switch s {
	case LifecycleActive:
		return 0
	case LifecycleMerged:
		return 1
	case LifecycleSuperseded:
		return 2
	default:
		return 3
	}
}

// ComputeEntityBoosts mirrors mem0/mem0/memory/main.py:1733-1813's entity boost
// computation, simplified to a pure function. Given query entities (deduplicated,
// capped at 8 like mem0) and a per-entity similarity lookup, it returns a map
// of instanceID → boost in [0, EntityBoostWeight].
//
// The lookup returns, for a given entity, the set of (instanceID, similarity)
// pairs whose entity embedding matched with similarity >= 0.5 (mem0's floor).
// We take the max boost across all entities per instance and apply mem0's
// memory_count_weight damping: 1/(1+0.001*(n-1)^2) so a single instance linked
// to many query entities does not get over-weighted.
func ComputeEntityBoosts(queryEntities []string, lookup func(entity string) []EntityHit) map[string]float64 {
	boosts := make(map[string]float64)
	if len(queryEntities) == 0 || lookup == nil {
		return boosts
	}

	// Cap at 8 query entities (mem0/mem0/memory/main.py:1745-1751).
	if len(queryEntities) > 8 {
		queryEntities = queryEntities[:8]
	}

	// Count how many entities hit each instance (for memory_count_weight damping).
	linkedCount := make(map[string]int)
	bestSim := make(map[string]float64)
	for _, ent := range queryEntities {
		hits := lookup(ent)
		for _, h := range hits {
			if h.Similarity < 0.5 {
				continue // mem0's similarity floor
			}
			linkedCount[h.InstanceID]++
			if h.Similarity > bestSim[h.InstanceID] {
				bestSim[h.InstanceID] = h.Similarity
			}
		}
	}

	for id, sim := range bestSim {
		n := linkedCount[id]
		memoryCountWeight := 1.0 / (1.0 + 0.001*float64((n-1)*(n-1)))
		boosts[id] = sim * EntityBoostWeight * memoryCountWeight
	}
	return boosts
}

// EntityHit is one (instance, similarity) pair for an entity match.
type EntityHit struct {
	InstanceID string
	Similarity float64
}
