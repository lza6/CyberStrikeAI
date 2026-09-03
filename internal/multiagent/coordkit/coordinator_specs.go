// Package coordkit
package coordkit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// LoadSpecs converts coordinator-emitted TaskSpecs into a fully resolved DAG:
// every spec gets a UUID, every DependsOn title/string token is resolved to a
// real task ID, and the resulting graph is validated for self-deps, unknown
// references, and cycles.
//
// This is the chicken-egg solution migrated from open-multi-agent-main's
// orchestrator.ts loadSpecsIntoQueue: the LLM emits readable title strings as
// dependency tokens (it cannot know UUIDs ahead of time), and we build a
// title→ID map on the fly. Two passes:
//  1. Create each task with a fresh UUID; record title→ID.
//  2. Resolve each spec's DependsOn tokens: try as a task ID first (tolerating
//     a model that returned the UUID), then as a title.
//
// Improvements over the reference project:
//   - Duplicate titles are rejected with a non-nil error (the TS map silently
//     overwrites, resolving later deps to the wrong task).
//   - Unresolved dependency tokens are rejected (the TS code silently drops
//     them, leaving tasks running without their declared predecessors).
//   - The graph is validated (the TS runTeam path never calls
//     validateTaskDependencies).
func LoadSpecs(specs []TaskSpec) (*DAG, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("LoadSpecs: no task specs")
	}
	d := &DAG{
		byID:    make(map[string]*Task, len(specs)),
		byTitle: make(map[string]string, len(specs)),
	}

	// Pass 1: create tasks, build title→ID index, reject duplicate titles.
	for i, spec := range specs {
		if strings.TrimSpace(spec.Title) == "" {
			return nil, fmt.Errorf("LoadSpecs: spec[%d] has empty title", i)
		}
		if strings.TrimSpace(spec.Desc) == "" {
			return nil, fmt.Errorf("LoadSpecs: spec[%d] %q has empty description", i, spec.Title)
		}
		key := normalizeTitleKey(spec.Title)
		if _, dup := d.byTitle[key]; dup {
			return nil, fmt.Errorf("LoadSpecs: duplicate task title %q", spec.Title)
		}
		id := newTaskID()
		task := &Task{
			ID:        id,
			Title:     strings.TrimSpace(spec.Title),
			Desc:      spec.Desc,
			Status:    TaskPending,
			Assignee:  strings.TrimSpace(spec.Assignee),
			DependsOn: nil,
			CreatedAt: nowNano(),
			UpdatedAt: nowNano(),
		}
		d.tasks = append(d.tasks, task)
		d.byID[id] = task
		d.byTitle[key] = id
	}

	// Pass 2: resolve each spec's DependsOn tokens to IDs.
	for i, spec := range specs {
		if len(spec.DependsOn) == 0 {
			continue
		}
		task := d.tasks[i]
		var resolved []string
		for _, depRef := range spec.DependsOn {
			ref := strings.TrimSpace(depRef)
			if ref == "" {
				continue
			}
			// Try as ID first (tolerate a model that returned the UUID).
			if _, ok := d.byID[ref]; ok {
				resolved = append(resolved, ref)
				continue
			}
			// Then as title.
			if id, ok := d.byTitle[normalizeTitleKey(ref)]; ok {
				resolved = append(resolved, id)
				continue
			}
			return nil, fmt.Errorf("LoadSpecs: task %q depends on unknown %q", task.Title, ref)
		}
		task.DependsOn = resolved
		task.UpdatedAt = nowNano()
	}

	if err := d.ValidateDependencies(); err != nil {
		return nil, fmt.Errorf("LoadSpecs: %w", err)
	}
	return d, nil
}

// ParseTaskSpecs parses the coordinator's raw text output into TaskSpecs.
//
// Migrated from orchestrator.ts parseTaskSpecs. Tolerant strategies:
//  1. ```json fenced block.
//  2. First '[' to last ']' (bare array).
//
// JSON.parse the slice, keep only items with string title+description, coerce
// assignee (string) and dependsOn (string array), return nil if nothing valid
// is found. Does NOT throw — callers use nil to trigger the fallback path.
func ParseTaskSpecs(raw string) []TaskSpec {
	candidate := raw
	if fence := extractFencedCodeBlock(raw, "json"); fence != "" {
		candidate = fence
	}
	arr, ok := sliceBalanced(strings.TrimSpace(candidate), '[', ']')
	if !ok {
		return nil
	}
	var parsed []any
	if err := json.Unmarshal([]byte(arr), &parsed); err != nil {
		return nil
	}
	if len(parsed) == 0 {
		return nil
	}
	var specs []TaskSpec
	for _, item := range parsed {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := obj["title"].(string)
		desc, _ := obj["description"].(string)
		if strings.TrimSpace(title) == "" || strings.TrimSpace(desc) == "" {
			continue
		}
		spec := TaskSpec{Title: title, Desc: desc}
		if a, ok := obj["assignee"].(string); ok {
			spec.Assignee = a
		}
		if deps, ok := obj["dependsOn"].([]any); ok {
			for _, d := range deps {
				if s, ok := d.(string); ok {
					spec.DependsOn = append(spec.DependsOn, s)
				}
			}
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil
	}
	return specs
}

// FallbackSpecs builds a task per known agent when the coordinator's output
// could not be parsed. Mirrors orchestrator.ts's fallback in runTeam: each
// agent gets one task whose description is the original goal. This keeps the
// pipeline runnable even on a malformed coordinator response.
func FallbackSpecs(goal string, agents []string) []TaskSpec {
	if len(agents) == 0 {
		return nil
	}
	specs := make([]TaskSpec, 0, len(agents))
	for _, a := range agents {
		specs = append(specs, TaskSpec{
			Title:    fmt.Sprintf("%s: %s", a, truncate(goal, 80)),
			Desc:     goal,
			Assignee: a,
		})
	}
	return specs
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
