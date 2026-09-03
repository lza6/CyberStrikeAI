// Package memory implements the mem0-inspired memory subsystem for CyberStrikeAI.
//
// It is intentionally structured to mirror internal/ctxindex: the core algorithms
// (ADD-only dedup, multi-signal fusion scoring, temporal reasoning) are pure
// functions free of any DB dependency so they can be unit-tested in a CGO-disabled
// environment (the production build is CGO_ENABLED=0, where mattn/go-sqlite3 is a
// stub and FTS5 is unavailable). The storage layer is a small interface backed by
// an in-memory fake for semantic tests and by a SQLite implementation for runtime.
//
// The four mem0 capabilities this package implements:
//
//  1. Single-pass ADD-only extraction — one LLM call produces ADD events; nothing
//     is ever overwritten. Duplicate facts (same content hash) are skipped, never
//     mutated. (mirrors mem0/mem0/memory/main.py:916-1206)
//  2. Multi-signal fusion retrieval — semantic (vector cosine) + BM25 + entity
//     boost, combined via an adaptive divisor and threshold gating.
//     (mirrors mem0/mem0/utils/scoring.py:43-139)
//  3. Temporal reasoning — dated instance ordering (current/past/planned) via
//     event_date + lifecycle_state (active/merged/superseded) + latest_only /
//     include_merged read modes. (mirrors mem0 platform docs, OSS has no impl)
//  4. Agent-confirmed facts stored with equal weight — attributed_to is a metadata
//     field only; it never changes storage, embedding, or scoring weight.
//     (mirrors mem0/mem0/memory/main.py:1036-1037)
package memory

import "time"

// LifecycleState enumerates the lifecycle of a fact instance.
//
//   - active      — current truth, participates in default retrieval
//   - merged      — folded into another instance (dedup/synthesis)
//   - superseded  — replaced by a newer instance; kept for history, linked via ReplacedBy
//
// This enum mirrors the mem0 platform contract (openapi.json:7984-7991).
type LifecycleState string

const (
	LifecycleActive     LifecycleState = "active"
	LifecycleMerged     LifecycleState = "merged"
	LifecycleSuperseded LifecycleState = "superseded"
)

// AttributedTo classifies the originator of a fact. Per mem0's "agent-generated
// facts are first-class" design, the value is metadata only: it does not change
// storage position, embedding, or scoring weight. (mem0 configs/prompts.py:935)
type AttributedTo string

const (
	AttributedUser      AttributedTo = "user"      // facts stated by or about the user
	AttributedAssistant AttributedTo = "assistant" // recommendations/plans/confirmations from the agent
	AttributedAuto      AttributedTo = "auto"      // facts extracted automatically (no explicit attribution)
)

// FactInstance is a single append-only memory record. Once written, a row is
// never UPDATEd in place — supersession writes a new active row and flips the
// old row's lifecycle_state to "superseded" with replaced_by pointing forward.
//
// Hash is the ADD-only dedup key (md5 of Memory); it is immutable and never
// recomputed. Version is the 1-based position in a fact_key's version chain.
//
// DIVERGENCE FROM mem0 OSS: mem0 relates memories via a graph of `linked_memory_ids`
// (prompts.py:513, main.py:626-629) and its ADDITIVE_EXTRACTION_PROMPT emits
// `id`/`text`/`attributed_to`/`linked_memory_ids`. This struct uses a `fact_key`
// version-chain model (same fact_key = version chain; supersede links forward
// via ReplacedBy) instead of the linked-memory graph, and `event_date` for
// dated-instance temporal reasoning (mem0 OSS has only `expiration_date`;
// event_date/lifecycle is a platform-only feature). `fact_key` + `event_date`
// are CyberStrikeAI extensions chosen to fit the existing project_facts model;
// they are not 1:1 mem0 OSS fields.
type FactInstance struct {
	ID                   string         `json:"id"`
	ProjectID            string         `json:"project_id"`
	FactKey              string         `json:"fact_key"` // logical grouping key (e.g. "host:10.0.0.1:os")
	Memory               string         `json:"memory"`   // the fact text (mem0's "data" field)
	Hash                 string         `json:"hash"`     // md5(Memory), ADD-only dedup key
	AttributedTo         AttributedTo   `json:"attributed_to"`
	EventDate            string         `json:"event_date,omitempty"` // ISO date "2026-09-02"; "" = undated
	LifecycleState       LifecycleState `json:"lifecycle_state"`
	ReplacedBy           string         `json:"replaced_by,omitempty"` // forward link to superseding instance ID
	Version              int            `json:"version"`               // 1-based within fact_key chain
	SourceConversationID string         `json:"source_conversation_id,omitempty"`
	SourceMessageID      string         `json:"source_message_id,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

// ReadMode controls superseded-instance visibility in retrieval.
//
//   - LatestOnly (default): return only active instances (current truth)
//   - IncludeMerged: return active + merged (folded duplicates surfaced)
//   - IncludeSuperseded: return active + merged + superseded (full history)
type ReadMode int

const (
	ReadLatestOnly        ReadMode = 0 // active only
	ReadIncludeMerged     ReadMode = 1 // active + merged
	ReadIncludeSuperseded ReadMode = 2 // active + merged + superseded (full chain)
)

// AddResult is the outcome of a single ADD attempt. Event is always "ADD" for a
// newly persisted instance; duplicates (same hash already present) return a
// Skipped result referencing the existing instance, never an UPDATE.
//
// This mirrors mem0/mem0/memory/main.py:1196 where the returned event is
// exclusively "ADD" — there is no UPDATE/DELETE event in ADD-only extraction.
type AddResult struct {
	Instance *FactInstance `json:"instance,omitempty"`
	Event    string         `json:"event"` // "ADD" | "SKIPPED_DUPLICATE"
	Skipped  bool           `json:"skipped"` // true when hash matched an existing instance
}

// Store is the storage abstraction for the memory subsystem. Implementations
// must enforce ADD-only semantics: Add never mutates an existing row, and
// Supersede creates a new active row while flipping the prior row's lifecycle.
type Store interface {
	// Add persists a new instance unless an instance with the same
	// (project_id, hash) already exists, in which case it returns a
	// Skipped result pointing at the existing instance. It never UPDATEs.
	Add(inst *FactInstance) (*AddResult, error)

	// Supersede marks oldID as superseded by newInst. newInst is persisted as
	// active (its own hash dedup applies first), then oldID.ReplacedBy is set
	// to newInst.ID and oldID.LifecycleState becomes superseded. The old row
	// is never deleted.
	Supersede(projectID, oldID string, newInst *FactInstance) (*AddResult, error)

	// Get returns a single instance by ID regardless of lifecycle state.
	Get(projectID, id string) (*FactInstance, error)

	// ListByFactKey returns the version chain for a fact_key in creation
	// order (oldest first). mode controls whether superseded/merged rows
	// are included; LatestOnly yields at most the active tail.
	ListByFactKey(projectID, factKey string, mode ReadMode) ([]*FactInstance, error)

	// ListActive returns all active instances for a project, optionally
	// filtered by fact_key prefix. This is the default retrieval pool.
	ListActive(projectID string, factKeyPrefix string) ([]*FactInstance, error)
}
