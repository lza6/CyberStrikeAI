package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryGraphStore 进程内 GraphStore 实现（map+RWMutex）。
// 用于单元测试、轻量部署、无数据库环境。无需 Init 即可用。
type MemoryGraphStore struct {
	mu    sync.RWMutex
	nodes map[string]*Entity
	edges map[[2]string]*Relation
}

// NewMemoryGraphStore 构造空存储。
func NewMemoryGraphStore() *MemoryGraphStore {
	return &MemoryGraphStore{
		nodes: make(map[string]*Entity),
		edges: make(map[[2]string]*Relation),
	}
}

func (m *MemoryGraphStore) Backend() string { return "memory" }

func (m *MemoryGraphStore) Init(ctx context.Context) error { return nil }

func (m *MemoryGraphStore) IndexDoneCallback(ctx context.Context) error { return nil }

func (m *MemoryGraphStore) Close() error { return nil }

func (m *MemoryGraphStore) Drop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = make(map[string]*Entity)
	m.edges = make(map[[2]string]*Relation)
	return nil
}

func (m *MemoryGraphStore) UpsertNode(ctx context.Context, e *Entity) error {
	if m == nil {
		return fmt.Errorf("memory graph store: nil")
	}
	if e == nil || strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("upsert node: empty name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name := strings.TrimSpace(e.Name)
	now := time.Now().UTC()
	if existing, ok := m.nodes[name]; ok {
		existing.Description = splitDescription(existing.Description, strings.TrimSpace(e.Description))
		existing.ChunkIDs = mergeStringSlicesUnique(existing.ChunkIDs, e.ChunkIDs)
		if strings.TrimSpace(e.Type) != "" {
			existing.Type = strings.TrimSpace(e.Type)
		}
		if strings.TrimSpace(e.SourceID) != "" {
			existing.SourceID = strings.TrimSpace(e.SourceID)
		}
		existing.UpdatedAt = now
		return nil
	}
	clone := *e
	clone.Name = name
	clone.ChunkIDs = mergeStringSlicesUnique(nil, e.ChunkIDs)
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = now
	}
	clone.UpdatedAt = now
	m.nodes[name] = &clone
	return nil
}

func (m *MemoryGraphStore) UpsertEdge(ctx context.Context, r *Relation) error {
	if m == nil {
		return fmt.Errorf("memory graph store: nil")
	}
	if r == nil || strings.TrimSpace(r.SrcID) == "" || strings.TrimSpace(r.TgtID) == "" {
		return fmt.Errorf("upsert edge: empty src/tgt")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := strings.TrimSpace(r.SrcID)
	tgt := strings.TrimSpace(r.TgtID)
	key := edgeKey(src, tgt)
	now := time.Now().UTC()
	if existing, ok := m.edges[key]; ok {
		existing.Description = splitDescription(existing.Description, strings.TrimSpace(r.Description))
		existing.ChunkIDs = mergeStringSlicesUnique(existing.ChunkIDs, r.ChunkIDs)
		existing.Weight += r.Weight
		if existing.Weight < 0 {
			existing.Weight = 0
		}
		if strings.TrimSpace(r.Keywords) != "" {
			existing.Keywords = strings.TrimSpace(r.Keywords)
		}
		if strings.TrimSpace(r.SourceID) != "" {
			existing.SourceID = strings.TrimSpace(r.SourceID)
		}
		existing.UpdatedAt = now
		return nil
	}
	clone := *r
	clone.SrcID, clone.TgtID = key[0], key[1]
	clone.ChunkIDs = mergeStringSlicesUnique(nil, r.ChunkIDs)
	if clone.ID == "" {
		clone.ID = fmt.Sprintf("edge-%s-%s", key[0], key[1])
	}
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = now
	}
	clone.UpdatedAt = now
	m.edges[key] = &clone
	return nil
}

func (m *MemoryGraphStore) GetNode(ctx context.Context, name string) (*Entity, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.nodes[strings.TrimSpace(name)]; ok {
		clone := *e
		return &clone, nil
	}
	return nil, nil
}

func (m *MemoryGraphStore) GetEdge(ctx context.Context, src, tgt string) (*Relation, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := edgeKey(src, tgt)
	if r, ok := m.edges[key]; ok {
		clone := *r
		return &clone, nil
	}
	return nil, nil
}

func (m *MemoryGraphStore) GetNodeEdges(ctx context.Context, name string) ([][2]string, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	name = strings.TrimSpace(name)
	out := [][2]string{}
	for k := range m.edges {
		if k[0] == name || k[1] == name {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out, nil
}

func (m *MemoryGraphStore) HasNode(ctx context.Context, name string) (bool, error) {
	n, err := m.GetNode(ctx, name)
	return n != nil, err
}

func (m *MemoryGraphStore) HasEdge(ctx context.Context, src, tgt string) (bool, error) {
	e, err := m.GetEdge(ctx, src, tgt)
	return e != nil, err
}

func (m *MemoryGraphStore) NodeDegree(ctx context.Context, name string) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("memory graph store: nil")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	name = strings.TrimSpace(name)
	n := 0
	for k := range m.edges {
		if k[0] == name || k[1] == name {
			n++
		}
	}
	return n, nil
}

func (m *MemoryGraphStore) NodeDegreesBatch(ctx context.Context, names []string) (map[string]int, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	out := make(map[string]int, len(names))
	for _, n := range names {
		d, err := m.NodeDegree(ctx, n)
		if err != nil {
			return nil, err
		}
		out[n] = d
	}
	return out, nil
}

func (m *MemoryGraphStore) GetNodesBatch(ctx context.Context, names []string) (map[string]*Entity, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	out := make(map[string]*Entity, len(names))
	for _, n := range names {
		e, err := m.GetNode(ctx, n)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out[n] = e
		}
	}
	return out, nil
}

func (m *MemoryGraphStore) GetEdgesBatch(ctx context.Context, pairs [][2]string) (map[[2]string]*Relation, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	out := make(map[[2]string]*Relation, len(pairs))
	for _, p := range pairs {
		r, err := m.GetEdge(ctx, p[0], p[1])
		if err != nil {
			return nil, err
		}
		if r != nil {
			out[edgeKey(p[0], p[1])] = r
		}
	}
	return out, nil
}

func (m *MemoryGraphStore) GetNodeEdgesBatch(ctx context.Context, names []string) (map[string][][2]string, error) {
	if m == nil {
		return nil, fmt.Errorf("memory graph store: nil")
	}
	out := make(map[string][][2]string, len(names))
	for _, n := range names {
		edges, err := m.GetNodeEdges(ctx, n)
		if err != nil {
			return nil, err
		}
		out[n] = edges
	}
	return out, nil
}

func (m *MemoryGraphStore) RemoveByItem(ctx context.Context, itemID string) error {
	if m == nil {
		return fmt.Errorf("memory graph store: nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	itemID = strings.TrimSpace(itemID)
	for k, r := range m.edges {
		if strings.TrimSpace(r.SourceID) == itemID {
			delete(m.edges, k)
		}
	}
	for name, e := range m.nodes {
		if strings.TrimSpace(e.SourceID) == itemID {
			delete(m.nodes, name)
		}
	}
	return nil
}

// mergeStringSlicesUnique 合并两个字符串切片去重（保持顺序）。
func mergeStringSlicesUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

var _ GraphStore = (*MemoryGraphStore)(nil)
