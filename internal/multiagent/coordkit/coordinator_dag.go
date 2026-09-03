// Package coordkit
package coordkit

import (
	"fmt"
	"strings"
)

// TaskStatus is the lifecycle state of a coordinator task. Mirrors
// open-multi-agent-main src/types.ts TaskStatus.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "in_progress"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskBlocked   TaskStatus = "blocked"
)

// Task is a coordinator decomposed unit of work. Mirrors task.ts Task.
// DependsOn holds resolved task IDs (UUIDs); the coordinator emits titles
// (or stable string IDs) which LoadSpecs resolves to these UUIDs.
type Task struct {
	ID         string
	Title      string
	Desc       string
	Status     TaskStatus
	Assignee   string // optional; empty means unassigned
	DependsOn  []string
	Result     string
	MaxRetries int
	RetryDelay int // milliseconds; 0 = 1000 default
	Backoff    float64
	CreatedAt  int64
	UpdatedAt  int64
}

// TaskSpec is the parsed coordinator output describing a single task before it
// is promoted into a full Task. Mirrors orchestrator.ts ParsedTaskSpec. The
// DependsOn field is the raw title/string token from the LLM; LoadSpecs
// resolves it to a real Task ID.
type TaskSpec struct {
	Title     string
	Desc      string
	Assignee  string
	DependsOn []string
}

// normalizeTitleKey produces the canonical lookup key for a task title:
// lower-cased and trimmed. Matches the reference project's
// titleToId case-insensitive matching in loadSpecsIntoQueue.
func normalizeTitleKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// DAG holds the resolved set of coordinator tasks plus a by-ID index. It is
// constructed by LoadSpecs from a slice of TaskSpec (the coordinator output)
// and carries the title→ID resolution that lets the coordinator emit
// human-readable title dependencies without knowing UUIDs up front.
//
// Improvement over the reference project: LoadSpecs also runs
// ValidateDependencies, which the TS runTeam path omits (a documented gap
// there). Unknown dependency references and dependency cycles are hard errors
// here rather than silently dropped.
type DAG struct {
	tasks   []*Task
	byID    map[string]*Task
	byTitle map[string]string // normalized title → task ID
}

// Tasks returns the tasks in load order.
func (d *DAG) Tasks() []*Task {
	out := make([]*Task, len(d.tasks))
	copy(out, d.tasks)
	return out
}

// ByID returns the task with the given ID, or nil.
func (d *DAG) ByID(id string) *Task {
	return d.byID[id]
}

// DependencyOrder returns the tasks in topological order (Kahn's algorithm).
// Tasks with no dependencies come first. A cycle yields a partial order plus
// a non-nil error describing the cycle. Unknown dependency references have
// already been rejected by LoadSpecs, so this function only needs to detect
// cycles among known IDs.
func (d *DAG) DependencyOrder() ([]*Task, error) {
	inDegree := make(map[string]int, len(d.tasks))
	successors := make(map[string][]string, len(d.tasks))
	for _, t := range d.tasks {
		if _, ok := inDegree[t.ID]; !ok {
			inDegree[t.ID] = 0
		}
		if _, ok := successors[t.ID]; !ok {
			successors[t.ID] = nil
		}
	}
	for _, t := range d.tasks {
		for _, dep := range t.DependsOn {
			if _, ok := d.byID[dep]; !ok {
				// Already rejected by LoadSpecs; defensive only.
				continue
			}
			inDegree[t.ID]++
			successors[dep] = append(successors[dep], t.ID)
		}
	}

	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var ordered []*Task
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ordered = append(ordered, d.byID[id])
		for _, succ := range successors[id] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	if len(ordered) != len(d.tasks) {
		// Remaining tasks form a cycle.
		var cyclic []*Task
		for _, t := range d.tasks {
			if inDegree[t.ID] > 0 {
				cyclic = append(cyclic, t)
			}
		}
		titles := make([]string, 0, len(cyclic))
		for _, t := range cyclic {
			titles = append(titles, t.Title)
		}
		return ordered, fmt.Errorf("dependency cycle detected among tasks: %v", titles)
	}
	return ordered, nil
}

// IsReady reports whether task can be started now: it must be pending and
// every task in DependsOn must be completed. Mirrors task.ts isTaskReady.
func (d *DAG) IsReady(task *Task) bool {
	if task.Status != TaskPending {
		return false
	}
	if len(task.DependsOn) == 0 {
		return true
	}
	for _, dep := range task.DependsOn {
		depTask, ok := d.byID[dep]
		if !ok || depTask.Status != TaskCompleted {
			return false
		}
	}
	return true
}

// ValidateDependencies checks the task graph for self-dependencies, unknown
// references, and cycles. Returns nil when the graph is sound. Mirrors
// task.ts validateTaskDependencies (which runTeam omits — we run it).
func (d *DAG) ValidateDependencies() error {
	if len(d.tasks) == 0 {
		return nil
	}
	// Pass 1: self-deps + unknown references.
	for _, t := range d.tasks {
		for _, dep := range t.DependsOn {
			if dep == t.ID {
				return fmt.Errorf("task %q (%s) depends on itself", t.Title, t.ID)
			}
			if _, ok := d.byID[dep]; !ok {
				return fmt.Errorf("task %q references unknown dependency %q", t.Title, dep)
			}
		}
	}
	// Pass 2: DFS three-color cycle detection.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(d.tasks))
	var stack []string
	var dfs func(id string) error
	dfs = func(id string) error {
		color[id] = gray
		stack = append(stack, id)
		t := d.byID[id]
		for _, dep := range t.DependsOn {
			switch color[dep] {
			case white:
				if err := dfs(dep); err != nil {
					return err
				}
			case gray:
				// back-edge = cycle
				cycleStart := 0
				for i, s := range stack {
					if s == dep {
						cycleStart = i
						break
					}
				}
				cycle := append([]string{}, stack[cycleStart:]...)
				cycle = append(cycle, dep)
				return fmt.Errorf("dependency cycle: %s", strings.Join(cycle, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}
	for _, t := range d.tasks {
		if color[t.ID] == white {
			if err := dfs(t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
