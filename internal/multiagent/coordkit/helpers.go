// Package coordkit
package coordkit

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// mutex is a named alias for sync.RWMutex so the rest of the package can refer
// to a single, consistent lock type without each file re-importing sync. We
// use an RWMutex because read-heavy access patterns (GetUnread/GetAll) benefit
// from concurrent readers, matching the reference project's read paths.
type mutex = sync.RWMutex

// nowNano returns the current time in unix nanoseconds. Wrapped so tests can
// substitute a fake clock by swapping this package-level variable.
var nowNano = func() int64 { return time.Now().UnixNano() }

// newMessageID returns a stable, unique message ID. UUID v4 matches the
// reference project's randomUUID() usage in messaging.ts.
func newMessageID() string {
	return uuid.NewString()
}

// newTaskID returns a stable, unique task ID for the DAG. UUID v4, matching
// task.ts createTask's randomUUID().
func newTaskID() string {
	return uuid.NewString()
}

// monotonicSeq is an internal counter used to disambiguate duplicate task
// titles during title→ID resolution (see coordinator_dag.go).
var titleDedupSeq atomic.Uint64

// nextTitleDedupSeq returns the next monotonically increasing disambiguator.
func nextTitleDedupSeq() uint64 { return titleDedupSeq.Add(1) }
