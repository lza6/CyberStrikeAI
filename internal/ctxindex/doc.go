// Package ctxindex implements the pure-logic core of the context-mode token
// efficiency subsystem: BM25 ranking, Reciprocal Rank Fusion, intent verdict
// construction, and a lightweight tokenizer. It is intentionally free of CGO
// and any third-party dependency so it can be unit-tested without a SQLite
// build toolchain.
//
// The runtime FTS5 store (see store.go) layers on top of these primitives and
// does require mattn/go-sqlite3 + the `fts5` build tag; that layer is only
// exercised in CGO-enabled environments. The algorithms here mirror the
// behaviour of SQLite's bm25() and the RRF/proximity rerank described in the
// context-mode reference project (store.ts:1244-1389).
package ctxindex

// Document is a unit of searchable content indexed by the context engine.
type Document struct {
	ID       string // stable identifier (event id, execution id, chunk hash)
	Title    string // short label presented to the model in verdicts
	Content  string // full text (only stored, never returned verbatim in verdicts)
	Source   string // attribution label, e.g. "execute:shell" or "session-events"
}

// Scored pairs a document with its ranking score; higher is more relevant.
type Scored struct {
	Doc   Document
	Score float64
}

// Hit is a ranked result ready to be rendered into a verdict.
type Hit struct {
	Doc    Document
	Score  float64
	Rank   int    // 1-based position within its source ranking
	Source string // which ranking produced this hit (for RRF debugging)
}
