package mcp

import (
	"sync"

	"cyberstrike-ai/internal/ctxsandbox"
)

// ctxEngineOnce guards the process-level singleton so ctx_execute and
// ctx_search share the same in-memory index store, closing the retrieval
// loop: what ctx_execute indexes, ctx_search retrieves.
var (
	ctxEngineOnce sync.Once
	ctxEngineInst *ctxsandbox.Engine
	ctxIndexInst  ctxsandbox.Index
)

// defaultCtxEngine returns the process-level singleton sandbox engine used by
// the ctx_execute/ctx_search MCP tools. The engine and its MemoryIndex are
// concurrency-safe (Engine has no mutable state; MemoryIndex uses an
// RWMutex), so a single shared instance is correct across goroutines and
// conversations.
//
// In a CGO-enabled build, a SQLite+FTS5-backed Index implementation would
// swap in here by replacing NewMemoryIndex() with the FTS5 store; the
// singleton wiring stays unchanged.
func defaultCtxEngine() *ctxsandbox.Engine {
	ctxEngineOnce.Do(func() {
		idx := ctxsandbox.NewMemoryIndex()
		ctxIndexInst = idx
		ctxEngineInst = &ctxsandbox.Engine{Index: idx}
	})
	return ctxEngineInst
}

// defaultCtxIndex returns the shared index store backing defaultCtxEngine.
// Exposed separately for callers (ctx_search) that only need the Index
// contract, not the Engine.
func defaultCtxIndex() ctxsandbox.Index {
	_ = defaultCtxEngine() // ensure initialised
	return ctxIndexInst
}
