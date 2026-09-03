package memory

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ContentHash returns the ADD-only dedup key for a fact's memory text.
//
// It is the lowercase hex md5 of the memory string — the exact scheme mem0 uses
// (mem0/mem0/memory/main.py:1020: hashlib.md5(text.encode()).hexdigest()). The
// hash is computed once at extraction time and stored immutably; dedup compares
// (project_id, hash) pairs. Because the hash is purely a function of memory text,
// two ADD operations with the same wording anywhere in the system collapse to a
// single stored instance — never an UPDATE, never a second row.
func ContentHash(memory string) string {
	sum := md5.Sum([]byte(memory))
	return hex.EncodeToString(sum[:])
}

// NormalizeAttributedTo coerces an arbitrary source string into a canonical
// AttributedTo. Unknown values map to "auto" — a fact whose origin the caller
// did not classify. This is the single chokepoint that enforces the "agent-
// confirmed facts are stored with equal weight" contract: the value is used
// purely as metadata, never to gate storage or scale scoring.
func NormalizeAttributedTo(s string) AttributedTo {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "user", "u":
		return AttributedUser
	case "assistant", "agent", "a":
		return AttributedAssistant
	default:
		return AttributedAuto
	}
}

// memoryStore is an in-memory Store implementation. It is the primary store for
// unit tests (no SQLite toolchain needed under CGO_ENABLED=0) and a fallback
// reference for the SQLite-backed runtime store.
type memoryStore struct {
	mu     sync.RWMutex
	rows   map[string]*FactInstance // id -> instance
	byHash map[string]string        // "projectID\x00hash" -> instanceID (dedup index)
}

// NewMemoryStore returns a Store backed by process-local maps. It is safe for
// concurrent use. All writes hold the write lock for the full op so supersede
// (which touches two rows) is atomic.
func NewMemoryStore() Store {
	return &memoryStore{
		rows:   make(map[string]*FactInstance),
		byHash: make(map[string]string),
	}
}

func hashKey(projectID, hash string) string { return projectID + "\x00" + hash }

func (s *memoryStore) Add(inst *FactInstance) (*AddResult, error) {
	if inst == nil {
		return nil, fmt.Errorf("memory: Add(nil instance)")
	}
	if strings.TrimSpace(inst.ProjectID) == "" {
		return nil, fmt.Errorf("memory: Add requires project_id")
	}
	if strings.TrimSpace(inst.Memory) == "" {
		return nil, fmt.Errorf("memory: Add requires non-empty memory")
	}
	if inst.Hash == "" {
		inst.Hash = ContentHash(inst.Memory)
	}
	if inst.LifecycleState == "" {
		inst.LifecycleState = LifecycleActive
	}
	if inst.Version == 0 {
		inst.Version = 1
	}
	now := time.Now().UTC()
	if inst.CreatedAt.IsZero() {
		inst.CreatedAt = now
	}
	inst.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()

	hk := hashKey(inst.ProjectID, inst.Hash)
	if existingID, ok := s.byHash[hk]; ok {
		existing := s.rows[existingID]
		return &AddResult{
			Instance: existing,
			Event:    "SKIPPED_DUPLICATE",
			Skipped:  true,
		}, nil
	}

	// New fact_key gets version 1; an existing chain bumps to max+1 so the
	// append-only model preserves every version even when content differs.
	inst.Version = s.nextVersionLocked(inst.ProjectID, inst.FactKey)

	if inst.ID == "" {
		inst.ID = newID()
	}
	s.rows[inst.ID] = inst
	s.byHash[hk] = inst.ID
	return &AddResult{Instance: inst, Event: "ADD"}, nil
}

func (s *memoryStore) Supersede(projectID, oldID string, newInst *FactInstance) (*AddResult, error) {
	if newInst == nil {
		return nil, fmt.Errorf("memory: Supersede(nil new instance)")
	}
	if strings.TrimSpace(oldID) == "" {
		return nil, fmt.Errorf("memory: Supersede requires oldID")
	}
	if strings.TrimSpace(newInst.ProjectID) == "" {
		newInst.ProjectID = projectID
	}
	if newInst.ProjectID != projectID {
		return nil, fmt.Errorf("memory: Supersede newInst.ProjectID %q != projectID %q", newInst.ProjectID, projectID)
	}
	if strings.TrimSpace(newInst.Memory) == "" {
		return nil, fmt.Errorf("memory: Supersede requires non-empty memory")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.rows[oldID]
	if !ok {
		return nil, fmt.Errorf("memory: Supersede old instance %q not found", oldID)
	}
	if old.ProjectID != projectID {
		return nil, fmt.Errorf("memory: Supersede project mismatch")
	}
	if old.LifecycleState == LifecycleSuperseded {
		return nil, fmt.Errorf("memory: Supersede target %q already superseded", oldID)
	}

	// Persist the new instance first via the same dedup path Add uses. If the
	// new content duplicates an existing hash, we still mark the old row
	// superseded — the "current truth" simply points at the pre-existing instance.
	if newInst.Hash == "" {
		newInst.Hash = ContentHash(newInst.Memory)
	}
	if newInst.LifecycleState == "" {
		newInst.LifecycleState = LifecycleActive
	}
	now := time.Now().UTC()
	if newInst.CreatedAt.IsZero() {
		newInst.CreatedAt = now
	}
	newInst.UpdatedAt = now
	newInst.Version = s.nextVersionLocked(projectID, newInst.FactKey)
	if newInst.ID == "" {
		newInst.ID = newID()
	}

	hk := hashKey(projectID, newInst.Hash)
	var result *AddResult
	if existingID, dup := s.byHash[hk]; dup {
		// New content already exists as another instance. The old row is still
		// superseded (its truth no longer holds); replaced_by points at the
		// pre-existing instance rather than a freshly minted duplicate.
		existing := s.rows[existingID]
		old.LifecycleState = LifecycleSuperseded
		old.ReplacedBy = existing.ID
		old.UpdatedAt = now
		result = &AddResult{Instance: existing, Event: "SKIPPED_DUPLICATE", Skipped: true}
	} else {
		s.rows[newInst.ID] = newInst
		s.byHash[hk] = newInst.ID
		old.LifecycleState = LifecycleSuperseded
		old.ReplacedBy = newInst.ID
		old.UpdatedAt = now
		result = &AddResult{Instance: newInst, Event: "ADD"}
	}
	return result, nil
}

func (s *memoryStore) Get(projectID, id string) (*FactInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst, ok := s.rows[id]
	if !ok || inst.ProjectID != projectID {
		return nil, fmt.Errorf("memory: instance %q not found in project %q", id, projectID)
	}
	return inst, nil
}

func (s *memoryStore) ListByFactKey(projectID, factKey string, mode ReadMode) ([]*FactInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*FactInstance
	for _, inst := range s.rows {
		if inst.ProjectID != projectID || inst.FactKey != factKey {
			continue
		}
		if !includeInMode(inst.LifecycleState, mode) {
			continue
		}
		out = append(out, inst)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

func (s *memoryStore) ListActive(projectID, factKeyPrefix string) ([]*FactInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*FactInstance
	for _, inst := range s.rows {
		if inst.ProjectID != projectID || inst.LifecycleState != LifecycleActive {
			continue
		}
		if factKeyPrefix != "" && !strings.HasPrefix(inst.FactKey, factKeyPrefix) {
			continue
		}
		out = append(out, inst)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].FactKey < out[j].FactKey
	})
	return out, nil
}

// nextVersionLocked computes the next 1-based version number for a fact_key
// chain within a project. Caller must hold the write lock.
func (s *memoryStore) nextVersionLocked(projectID, factKey string) int {
	maxVer := 0
	for _, inst := range s.rows {
		if inst.ProjectID == projectID && inst.FactKey == factKey && inst.Version > maxVer {
			maxVer = inst.Version
		}
	}
	return maxVer + 1
}

// includeInMode reports whether a lifecycle state is visible under the given
// read mode. active is always visible; merged requires IncludeMerged+; superseded
// requires IncludeSuperseded. This is the core of latest_only/include_merged
// read modes from the mem0 platform dream.mdx:80-88 table.
func includeInMode(state LifecycleState, mode ReadMode) bool {
	switch state {
	case LifecycleActive:
		return true
	case LifecycleMerged:
		return mode >= ReadIncludeMerged
	case LifecycleSuperseded:
		return mode >= ReadIncludeSuperseded
	default:
		return false
	}
}

// newID returns a v4-style random hex string. We avoid importing uuid here to
// keep the memory package free of a second direct dep (the SQLite store and
// the extractor both already bring uuid via internal/database; the in-memory
// store is the fallback path and only needs uniqueness, not RFC compliance).
// crypto/rand is used (not math/rand) so uniqueness holds under concurrency.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read practically never errors on the crypto/rand source; if it
		// does, fall back to a time-derived ID so Add is still total.
		return fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	// RFC 4122 v4 variant bits, purely for visual recognizability.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return "mem-" + hex.EncodeToString(b[:])
}
