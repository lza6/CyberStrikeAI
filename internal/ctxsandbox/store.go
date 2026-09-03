package ctxsandbox

import (
	"strings"
	"sync"

	"cyberstrike-ai/internal/ctxindex"
)

// MemoryIndex is a CGO-free in-memory implementation of the Index contract.
// It chunks content by lines (bounded per chunk) and ranks via the pure-logic
// BM25 in internal/ctxindex. In a CGO-enabled build, a SQLite+FTS5-backed
// implementation would swap in here with identical behaviour.
//
// Concurrency: all methods are goroutine-safe via a single RWMutex. The
// store is append-only — IndexPlaintext never overwrites prior content, so
// repeated indexing of the same output is safe (produces duplicate chunks
// that BM25 naturally ranks together).
type MemoryIndex struct {
	mu     sync.RWMutex
	docs   []ctxindex.Document
	labels map[string]string // chunk-id → source label
}

// NewMemoryIndex returns an empty in-memory index.
func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{labels: make(map[string]string)}
}

// IndexPlaintext splits content into line-bounded chunks (max ~2KB each to
// keep chunks retrieval-friendly) and stores them under the given source
// label. The label is returned verbatim so callers can hand it to the model
// as a spill reference.
func (m *MemoryIndex) IndexPlaintext(content, source string) (int, string) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "execute:unknown"
	}
	if content == "" {
		return 0, source
	}
	lines := strings.Split(content, "\n")
	chunkSize := 24 // lines per chunk ≈ 1-2KB for typical tool output
	if chunkSize < 1 {
		chunkSize = 1
	}
	count := 0
	chunk := make([]string, 0, chunkSize)
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		body := strings.Join(chunk, "\n")
		title := strings.TrimSpace(chunk[0])
		if len(title) > 120 {
			title = title[:120]
		}
		m.mu.Lock()
		idx := len(m.docs)
		m.docs = append(m.docs, ctxindex.Document{
			ID:      labelChunkID(source, idx),
			Title:   title,
			Content: body,
			Source:  source,
		})
		m.labels[m.docs[idx].ID] = source
		m.mu.Unlock()
		count++
		chunk = chunk[:0]
	}
	for _, ln := range lines {
		chunk = append(chunk, ln)
		if len(chunk) >= chunkSize {
			flush()
		}
	}
	flush()
	return count, source
}

// Search ranks stored chunks against the query using BM25, scoped to source
// when non-empty.
func (m *MemoryIndex) Search(query, source string, maxResults int) []ctxindex.Scored {
	m.mu.RLock()
	defer m.mu.RUnlock()
	source = strings.TrimSpace(source)
	var docs []ctxindex.Document
	for _, d := range m.docs {
		if source == "" || d.Source == source {
			docs = append(docs, d)
		}
	}
	scored := ctxindex.BM25Scores(docs, query, ctxindex.BM25Options{})
	if maxResults <= 0 || maxResults > len(scored) {
		maxResults = len(scored)
	}
	if maxResults > len(scored) {
		maxResults = len(scored)
	}
	if maxResults <= 0 {
		return nil
	}
	out := make([]ctxindex.Scored, maxResults)
	copy(out, scored[:maxResults])
	return out
}

// Size returns the number of indexed chunks.
func (m *MemoryIndex) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.docs)
}

func labelChunkID(source string, idx int) string {
	// Stable, human-readable id: source#index.
	return source + "#" + itoaSimple(idx)
}

// itoaSimple is a tiny non-negative int→string to avoid pulling strconv into
// this hot path unnecessarily. Falls back to manual digit conversion.
func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoaSimple(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
