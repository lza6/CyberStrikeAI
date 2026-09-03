package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/ctxindex"
)

// Service is the unified facade over the memory subsystem. It wires the four
// mem0 capabilities together:
//
//   - ADD-only extraction (single LLM call → Store.Add with hash dedup)
//   - Multi-signal fusion retrieval (semantic + BM25 + entity boost)
//   - Temporal reasoning (dated instance ordering, latest_only read modes)
//   - Agent-confirmed facts stored with equal weight (attributed_to metadata)
//
// Callers that want the full mem0 pipeline use Ingest() and Retrieve(). Lower
// -level orchestration can drive Extract / ScoreAndRank / ApplyTemporalAdjustment
// directly — each is exported and unit-tested.
type Service struct {
	store        Store
	llm          LLMClient
	semantic     SemanticSearcher // optional; nil disables vector signal
	entityIndex  EntitySearcher   // optional; nil disables entity signal
	now          func() time.Time
}

// SemanticSearcher returns the semantic (vector cosine) similarity of each
// candidate instance to a query. Implementations typically embed the query
// and compare against pre-stored instance embeddings. The signal is the
// primary retrieval channel: candidates below the fusion threshold are
// dropped before BM25/entity can rescue them.
type SemanticSearcher interface {
	// Search returns (instanceID → cosine similarity in [0,1]) for the query.
	Search(ctx context.Context, projectID, query string) (map[string]float64, error)
}

// EntitySearcher returns, for a query's entities, the instances linked to
// each entity and their similarity. Used by ComputeEntityBoosts.
type EntitySearcher interface {
	QueryEntities(ctx context.Context, projectID string, entities []string) (map[string][]EntityHit, error)
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithSemanticSearcher enables the vector signal. Without it, fusion falls
// back to BM25 + entity only (maxPossible divisor drops to 1.5 or 0.5+entity).
func WithSemanticSearcher(s SemanticSearcher) ServiceOption {
	return func(svc *Service) { svc.semantic = s }
}

// WithEntitySearcher enables the entity boost signal.
func WithEntitySearcher(s EntitySearcher) ServiceOption {
	return func(svc *Service) { svc.entityIndex = s }
}

// WithClock injects a clock for deterministic temporal tests.
func WithClock(fn func() time.Time) ServiceOption {
	return func(svc *Service) { svc.now = fn }
}

// NewService assembles a memory Service. store and llm are required; the
// semantic and entity signals are optional and fall back gracefully.
func NewService(store Store, llm LLMClient, opts ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("memory: NewService requires non-nil Store")
	}
	svc := &Service{
		store: store,
		llm:   llm,
		now:   time.Now,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc, nil
}

// Store exposes the underlying store for callers that want to Supersede or
// query version chains directly. Retrieval should go through Retrieve() so
// fusion and temporal reasoning are applied.
func (s *Service) Store() Store { return s.store }

// Ingest runs the single-pass ADD-only extraction: one LLM call produces
// candidate memories, each is ADDed with hash dedup. Never UPDATE, never
// DELETE. This is the entry point for the "边渗透边记录" cadence the
// project prompt already instructs agents to follow.
func (s *Service) Ingest(ctx context.Context, projectID, query string, recentMessages, existingMemories []string, observationDate string) ([]AddResult, error) {
	if s.llm == nil {
		return nil, fmt.Errorf("memory: Ingest requires an LLMClient (configure one or call Extract with a stub)")
	}
	curDate := ""
	if s.now != nil {
		curDate = s.now().UTC().Format("2006-01-02")
	}
	return ExtractAndStore(ctx, s.llm, s.store, projectID, query, recentMessages, existingMemories, observationDate, curDate)
}

// RetrieveOptions tunes the multi-signal fusion + temporal retrieval pass.
type RetrieveOptions struct {
	// Threshold is the minimum semantic score for a candidate to survive the
	// fusion gate. 0 means no gate (all active instances are candidates).
	Threshold float64
	// TopK caps the returned results; 0 defaults to 10.
	TopK int
	// ReadMode controls superseded visibility (default LatestOnly).
	ReadMode ReadMode
	// IncludeTemporal applies the additive temporal pass after fusion.
	IncludeTemporal bool
}

// Retrieve runs the full multi-signal pipeline against a project's memories:
//
//  1. Gather the candidate pool from the store (active by default; superseded
//     only when ReadMode allows).
//  2. Score semantic similarity for each candidate (if a SemanticSearcher is
//     configured).
//  3. Score BM25 over the pool using internal/ctxindex.BM25Scores (the pure-
//     Go Okapi we already ship, avoiding the FTS5 dependency that CGO_ENABLED=0
//     cannot satisfy).
//  4. Compute entity boosts (if an EntitySearcher is configured).
//  5. ScoreAndRank fuses the three signals with adaptive divisor + threshold gate.
//  6. ApplyTemporalAdjustment nudges scores by the dated-instance delta.
//
// The result is sorted best-first and truncated to TopK.
func (s *Service) Retrieve(ctx context.Context, projectID, query string, opts RetrieveOptions) ([]FusionResult, error) {
	if opts.TopK == 0 {
		opts.TopK = 10
	}
	if opts.ReadMode == 0 {
		opts.ReadMode = ReadLatestOnly
	}

	// 1. Candidate pool. We default to active only (the "current truth");
	// the caller opts into history via ReadMode. ListActive returns only
	// LifecycleActive rows; for IncludeMerged/IncludeSuperseded we augment
	// with merged (and superseded) instances by scanning each distinct
	// fact_key's version chain. This mirrors mem0's dream.mdx:80-88 read
	// modes: latest_only vs include_merged vs include_superseded.
	pool, err := s.store.ListActive(projectID, "")
	if err != nil {
		return nil, fmt.Errorf("memory: Retrieve ListActive: %w", err)
	}
	if opts.ReadMode >= ReadIncludeMerged {
		// Augment with non-active lifecycle states the caller asked for.
		// We scan per distinct fact_key in the active pool, pulling the full
		// chain and keeping the states the read mode permits.
		seen := map[string]bool{}
		for _, inst := range pool {
			seen[inst.FactKey] = true
		}
		for fk := range seen {
			chain, _ := s.store.ListByFactKey(projectID, fk, opts.ReadMode)
			for _, inst := range chain {
				if inst.LifecycleState == LifecycleActive {
					continue // already in pool
				}
				pool = append(pool, inst)
			}
		}
	}
	if len(pool) == 0 {
		return nil, nil
	}

	// 2. Semantic scores (optional signal).
	semScores := map[string]float64{}
	if s.semantic != nil {
		semScores, err = s.semantic.Search(ctx, projectID, query)
		if err != nil {
			return nil, fmt.Errorf("memory: semantic search: %w", err)
		}
	}

	// 3. BM25 over the pool. We build ctxindex.Documents (title=fact_key,
	// content=memory) and let the pure-Go Okapi rank them.
	docs := make([]ctxindex.Document, 0, len(pool))
	for _, inst := range pool {
		docs = append(docs, ctxindex.Document{
			ID:      inst.ID,
			Title:   inst.FactKey,
			Content: inst.Memory,
		})
	}
	bm25Scored := ctxindex.BM25Scores(docs, query, ctxindex.BM25Options{})
	bm25ByID := map[string]float64{}
	for _, sc := range bm25Scored {
		bm25ByID[sc.Doc.ID] = sc.Score
	}

	// 4. Entity boosts (optional signal).
	var entityBoosts map[string]float64
	if s.entityIndex != nil {
		entities := extractQueryEntities(query)
		if len(entities) > 0 {
			hitsPerEntity, err := s.entityIndex.QueryEntities(ctx, projectID, entities)
			if err != nil {
				return nil, fmt.Errorf("memory: entity search: %w", err)
			}
			lookup := func(entity string) []EntityHit {
				return hitsPerEntity[entity]
			}
			entityBoosts = ComputeEntityBoosts(entities, lookup)
		}
	}

	// 5. Assemble fusion candidates.
	cands := make([]FusionCandidate, 0, len(pool))
	for _, inst := range pool {
		c := FusionCandidate{Instance: inst}
		if s.semantic != nil {
			c.SemanticScore = semScores[inst.ID]
		} else {
			// Without a semantic searcher, treat all active instances as
			// equally semantically relevant (1.0) so BM25/entity drive ranking.
			c.SemanticScore = 1.0
		}
		c.BM25RawScore = bm25ByID[inst.ID]
		if entityBoosts != nil {
			c.EntityBoost = entityBoosts[inst.ID]
		}
		cands = append(cands, c)
	}

	// 6. Score + rank.
	results := ScoreAndRank(cands, query, ScoreAndRankOptions{
		Threshold: opts.Threshold,
		TopK:      opts.TopK,
		Explain:   true,
	})

	// ScoreAndRank with Explain=true keeps threshold-gated candidates in the
	// output (at score 0) so callers can audit what was dropped. For the
	// service's public Retrieve API, those gated candidates are not useful
	// results — filter them out before returning. The remaining results keep
	// their Explanation populated for debugging.
	filtered := results[:0]
	for _, r := range results {
		if !r.Explanation.SemanticBelowThreshold {
			filtered = append(filtered, r)
		}
	}
	results = filtered

	// 7. Temporal pass (additive, never filters).
	if opts.IncludeTemporal {
		now := s.now()
		intent := ClassifyTemporalIntent(query)
		queryDate := dateFromQueryForService(query)
		results = ApplyTemporalAdjustment(results, intent, queryDate, now)
	}

	return results, nil
}

// dateFromQueryForService extracts a YYYY-MM-DD date token from a query for
// the AtDate temporal branch. It wraps the package-level dateFromQuery so the
// service's call sites stay readable.
func dateFromQueryForService(q string) string {
	return dateFromQuery(strings.ToLower(q))
}

// extractQueryEntities is a deliberately simple entity extractor: it pulls
// quoted strings and CamelCase / UPPER tokens from the query. This is the Go
// fallback for when no spaCy-style NER is configured (the production build
// cannot depend on Python spaCy).
//
// It mirrors the spirit of mem0/mem0/utils/entity_extraction.py's QUOTED and
// IDENTIFIER categories without the spaCy NER dependency.
func extractQueryEntities(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}

	// Quoted spans.
	inQuote := false
	var cur strings.Builder
	for _, r := range q {
		switch {
		case r == '"' || r == '\'':
			if inQuote {
				if s := strings.TrimSpace(cur.String()); s != "" && !seen[s] {
					out = append(out, s)
					seen[s] = true
				}
				cur.Reset()
				inQuote = false
			} else {
				inQuote = true
			}
		case inQuote:
			cur.WriteRune(r)
		}
	}

	// CamelCase / UPPER identifiers (CVE-2026-1234, TeslaModel3, etc.).
	var token strings.Builder
	flush := func() {
		s := token.String()
		token.Reset()
		if s == "" {
			return
		}
		// CVE-XXXX-XXXXX, TeslaModel3, IPAddress, etc.
		if isEntityToken(s) && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	for _, r := range q {
		if r == '"' || r == '\'' {
			flush()
			continue
		}
		if isTokenRune(r) {
			token.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func isTokenRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

func isEntityToken(s string) bool {
	if len(s) < 3 {
		return false
	}
	// Contains a capital letter and is not a common stopword.
	hasUpper := false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper && !strings.Contains(s, "-") {
		return false
	}
	switch strings.ToLower(s) {
	case "the", "and", "for", "with", "this", "that", "from", "what", "when", "where", "how":
		return false
	}
	return true
}
