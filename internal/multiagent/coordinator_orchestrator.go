// Package multiagent
package multiagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/multiagent/coordkit"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// CoordinatorAgentFactory builds a one-shot adk.Agent for the coordinator role
// (decomposition and synthesis). The coordinator needs no tools — it only
// reasons over text. Migrated from open-multi-agent-main orchestrator.ts
// buildAgent(coordinatorConfig) + coordinatorAgent.run.
type CoordinatorAgentFactory func(ctx context.Context, instruction string) (adk.Agent, error)

// CoordinatorWorkerFactory builds a one-shot adk.Agent for a worker assigned to
// `assignee`. The worker carries the tools/middleware appropriate to its role.
// Migrated from orchestrator.ts buildPool → buildAgent(effectiveConfig).
type CoordinatorWorkerFactory func(ctx context.Context, assignee string) (adk.Agent, error)

// CoordinatorConfig tunes the CoordinatorRunner.
type CoordinatorConfig struct {
	AgentFactory  CoordinatorAgentFactory
	WorkerFactory CoordinatorWorkerFactory
	Bus           *coordkit.MessageBus
	Logger        *zap.Logger
	// MaxParallel caps concurrent worker dispatch. <=0 defaults to 5, matching
	// the reference project's DEFAULT_MAX_CONCURRENCY.
	MaxParallel int
	// KnownAgents is the roster the coordinator is told about in its system
	// prompt (worker names). Workers are built on demand by WorkerFactory.
	KnownAgents []string
	// SchedulerStrategy K9：调度策略（round-robin/least-busy/capability-match/
	// dependency-first）。空走 coordkit.DefaultSchedulerStrategy（round-robin，
	// 向后兼容）。四策略 + MaxParallel 信号量限并发，只改"先派谁"不改"派多少"。
	SchedulerStrategy coordkit.SchedulerStrategy
	// Scheduler K9：可注入的调度器（测试用）。nil 时按 SchedulerStrategy 构造。
	Scheduler *coordkit.Scheduler
}

// CoordinatorResult is the aggregated outcome of a coordinator run, mirroring
// open-multi-agent-main TeamRunResult (success + per-task results + final
// synthesis text).
type CoordinatorResult struct {
	Success      bool
	Response     string
	Tasks        []*coordkit.Task
	Specs        []coordkit.TaskSpec
	FallbackUsed bool
}

// CoordinatorRunner executes the runTeam pattern: coordinator decomposes the
// goal into a title-DAG of tasks, independent tasks run in parallel up to
// MaxParallel, dependents unblock as predecessors complete, and a final
// synthesis call produces the response. Migrated from orchestrator.ts runTeam.
type CoordinatorRunner struct {
	cfg CoordinatorConfig
}

// NewCoordinatorRunner constructs a runner. AgentFactory and WorkerFactory
// are required; Bus defaults to a fresh bus when nil.
func NewCoordinatorRunner(cfg CoordinatorConfig) (*CoordinatorRunner, error) {
	if cfg.AgentFactory == nil {
		return nil, fmt.Errorf("coordinator: AgentFactory is required")
	}
	if cfg.WorkerFactory == nil {
		return nil, fmt.Errorf("coordinator: WorkerFactory is required")
	}
	if cfg.Bus == nil {
		cfg.Bus = coordkit.NewMessageBus()
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 5
	}
	// K9：构造调度器。注入优先；否则按 SchedulerStrategy 构造。
	if cfg.Scheduler == nil {
		cfg.Scheduler = coordkit.NewScheduler(cfg.SchedulerStrategy)
	}
	return &CoordinatorRunner{cfg: cfg}, nil
}

// Run executes the full decompose → dispatch → synthesize flow.
func (r *CoordinatorRunner) Run(ctx context.Context, goal string) (*CoordinatorResult, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("coordinator: goal is empty")
	}

	// Step 1: coordinator decomposes the goal into task specs.
	decompInstruction := buildCoordinatorSystemPrompt(r.cfg.KnownAgents)
	coordinatorAgent, err := r.cfg.AgentFactory(ctx, decompInstruction)
	if err != nil {
		return nil, fmt.Errorf("coordinator: build decompose agent: %w", err)
	}
	decompPrompt := buildDecompositionPrompt(goal, r.cfg.KnownAgents)
	decompText, err := invokeAgentText(ctx, coordinatorAgent, decompPrompt)
	if err != nil {
		return nil, fmt.Errorf("coordinator: decompose invoke: %w", err)
	}

	// Step 2: parse specs; fall back to one-task-per-agent on parse failure.
	specs := coordkit.ParseTaskSpecs(decompText)
	result := &CoordinatorResult{Specs: specs}
	if len(specs) == 0 {
		specs = coordkit.FallbackSpecs(goal, r.cfg.KnownAgents)
		result.FallbackUsed = true
		result.Specs = specs
		if r.cfg.Logger != nil {
			r.cfg.Logger.Warn("coordinator: decomposition unparsable, using fallback",
				zap.Int("fallback_tasks", len(specs)))
		}
	}

	// Step 3: resolve title dependencies into a validated DAG.
	dag, err := coordkit.LoadSpecs(specs)
	if err != nil {
		return nil, fmt.Errorf("coordinator: load specs: %w", err)
	}

	// Step 4: round-based concurrent dispatch of ready tasks.
	if err := r.dispatchQueue(ctx, dag); err != nil {
		return nil, fmt.Errorf("coordinator: dispatch: %w", err)
	}

	// Step 5: synthesis from completed/failed task results.
	synthPrompt := buildSynthesisPrompt(goal, dag.Tasks())
	synthText, err := invokeAgentText(ctx, coordinatorAgent, synthPrompt)
	if err != nil {
		return nil, fmt.Errorf("coordinator: synthesis invoke: %w", err)
	}

	result.Response = synthText
	result.Tasks = dag.Tasks()
	result.Success = allTasksSucceeded(dag.Tasks())
	return result, nil
}

// dispatchQueue runs the round-based execution loop migrated from
// orchestrator.ts executeQueue: each round, all currently-ready pending tasks
// are dispatched in parallel (bounded by MaxParallel), and the loop repeats
// until no pending task remains (either all completed or the rest are
// blocked by a failed dependency).
//
// K9：接入 Scheduler 四策略（round-robin/least-busy/capability-match/
// dependency-first）。调度器按策略对 ready 任务排序，MaxParallel 信号量
// 仍限并发——四策略改变的是"先派谁"，不是"派多少"，与现有批次模式兼容。
// runningAssignees 跟踪当前 in_progress 任务的 assignee 计数，供
// least-busy/capability-match 策略使用。
func (r *CoordinatorRunner) dispatchQueue(ctx context.Context, dag *coordkit.DAG) error {
	sem := make(chan struct{}, r.cfg.MaxParallel)
	for {
		var ready []*coordkit.Task
		for _, t := range dag.Tasks() {
			if dag.IsReady(t) {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			break
		}
		// K9：按调度策略对 ready 排序。round-robin（默认）与改造前等价。
		runningAssignees := r.snapshotRunningAssignees(dag)
		ready = r.cfg.Scheduler.Select(dag, ready, runningAssignees)
		var wg sync.WaitGroup
		for _, task := range ready {
			task.Status = coordkit.TaskRunning
		}
		for _, task := range ready {
			wg.Add(1)
			go func(t *coordkit.Task) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					t.Status = coordkit.TaskFailed
					t.Result = "cancelled: context done"
					return
				}
				defer func() { <-sem }()
				r.runOneTask(ctx, t)
			}(task)
		}
		wg.Wait()
	}
	return nil
}

// snapshotRunningAssignees 返回当前 in_progress 任务的 assignee 计数快照。
// 供 least-busy/capability-match 策略决策（不持锁，快照一致性由 dispatchQueue
// 单轮串行保证：每轮 wg.Wait 后下一轮才重新快照）。
func (r *CoordinatorRunner) snapshotRunningAssignees(dag *coordkit.DAG) map[string]int {
	out := make(map[string]int)
	for _, t := range dag.Tasks() {
		if t.Status == coordkit.TaskRunning {
			out[t.Assignee]++
		}
	}
	return out
}

// runOneTask builds a worker for the task's assignee, injects any messages
// addressed to it from the bus, invokes the worker, and records the result.
//
// K9：消费 Task 的 MaxRetries / RetryDelay / Backoff 字段。
// 失败时按 computeRetryDelay(baseDelay, backoff, attempt, cap 30s) 退避重试。
// MaxRetries<=0 时不重试（维持单次执行向后兼容）。
func (r *CoordinatorRunner) runOneTask(ctx context.Context, t *coordkit.Task) {
	assignee := strings.TrimSpace(t.Assignee)
	if assignee == "" {
		t.Status = coordkit.TaskFailed
		t.Result = fmt.Sprintf("task %q has no assignee", t.Title)
		return
	}
	worker, err := r.cfg.WorkerFactory(ctx, assignee)
	if err != nil {
		t.Status = coordkit.TaskFailed
		t.Result = fmt.Sprintf("build worker %q: %v", assignee, err)
		return
	}
	prompt := buildTaskPrompt(t, r.cfg.Bus)

	// K9 retry/backoff 循环：最多尝试 1 + MaxRetries 次。
	// MaxRetries<=0 等价单次（0 次重试）。
	maxAttempts := 1 + t.MaxRetries
	baseDelay := time.Duration(t.RetryDelay) * time.Millisecond
	if baseDelay <= 0 {
		baseDelay = time.Second // 默认 1s（与 Task.RetryDelay 注释 0=1000ms 对齐）
	}
	backoff := t.Backoff
	if backoff <= 0 {
		backoff = 2.0
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// 上一次失败后退避等待（首次 attempt=0 不等）。
		if attempt > 0 {
			delay := coordkit.ComputeRetryDelay(baseDelay, backoff, attempt-1, coordkit.RetryBackoffCap)
			if r.cfg.Logger != nil {
				r.cfg.Logger.Info("coordinator task retry backoff",
					zap.String("task", t.Title),
					zap.Int("attempt", attempt),
					zap.Duration("delay", delay))
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				t.Status = coordkit.TaskFailed
				t.Result = "cancelled: context done during retry backoff"
				return
			}
		}

		out, err := invokeAgentText(ctx, worker, prompt)
		if err == nil {
			t.Status = coordkit.TaskCompleted
			t.Result = out
			return
		}

		// 本次失败：记录结果，待下一轮重试（若还有剩余 attempt）。
		t.Result = fmt.Sprintf("worker %q invoke: %v", assignee, err)
	}

	// 所有重试耗尽：标 TaskFailed。
	t.Status = coordkit.TaskFailed
}

// invokeAgentText runs a one-shot adk.Agent against a single user message and
// returns the final assistant text. Migrated from the test pattern in
// eino_agentic_chat_model_agent_test.go:77-96 (agent.Run → iterate →
// last.Output.MessageOutput.Message.Content). This is the synchronous
// "invoke one agent, get text" primitive the coordinator needs; the wider
// codebase has no such helper (confirmed by K0 reconnaissance).
func invokeAgentText(ctx context.Context, agent adk.Agent, userMessage string) (string, error) {
	iter := agent.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage(userMessage)},
	})
	if iter == nil {
		return "", fmt.Errorf("agent.Run returned nil iterator")
	}
	var last *adk.AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			return "", ev.Err
		}
		last = ev
	}
	if last == nil || last.Output == nil || last.Output.MessageOutput == nil {
		return "", fmt.Errorf("agent produced no message output")
	}
	return last.Output.MessageOutput.Message.Content, nil
}

// buildCoordinatorSystemPrompt renders the coordinator's system prompt.
// Migrated from orchestrator.ts buildCoordinatorSystemPrompt: it lists the
// roster and demands a JSON array of task specs whose dependsOn are task
// titles (the chicken-egg token the DAG resolves to IDs in LoadSpecs).
func buildCoordinatorSystemPrompt(agents []string) string {
	var b strings.Builder
	b.WriteString("You are the coordinator of a multi-agent team. ")
	b.WriteString("Break the user's goal into a dependency-ordered set of tasks ")
	b.WriteString("and assign each to one of the available agents.\n\n")
	b.WriteString("Available agents:\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s\n", a)
	}
	b.WriteString("\nReturn ONLY a JSON array of task objects, wrapped in a ```json code fence. ")
	b.WriteString("Do not include any text outside the fence. Each task object must have:\n")
	b.WriteString("- \"title\": a short, unique, human-readable title (other tasks reference this in dependsOn)\n")
	b.WriteString("- \"description\": the work to perform\n")
	b.WriteString("- \"assignee\": one of the agent names listed above\n")
	b.WriteString("- \"dependsOn\": array of titles of tasks this task depends on (empty array if none)\n")
	return b.String()
}

// buildDecompositionPrompt renders the user prompt for the decomposition call.
// Migrated from orchestrator.ts buildDecompositionPrompt.
func buildDecompositionPrompt(goal string, agents []string) string {
	var b strings.Builder
	b.WriteString("Goal: ")
	b.WriteString(goal)
	b.WriteString("\n\nAgents: ")
	b.WriteString(strings.Join(agents, ", "))
	b.WriteString("\n\nReturn ONLY the JSON task array in a ```json code fence.")
	return b.String()
}

// buildSynthesisPrompt renders the prompt for the synthesis call. Migrated
// from orchestrator.ts buildSynthesisPrompt: completed and failed task results
// are summarized, and the coordinator is asked to synthesize a final answer.
func buildSynthesisPrompt(goal string, tasks []*coordkit.Task) string {
	var b strings.Builder
	b.WriteString("Original goal: ")
	b.WriteString(goal)
	b.WriteString("\n\nTask results:\n\n")
	for _, t := range tasks {
		if t.Status == coordkit.TaskCompleted {
			fmt.Fprintf(&b, "### %s (completed by %s)\n%s\n\n", t.Title, t.Assignee, t.Result)
		} else if t.Status == coordkit.TaskFailed {
			fmt.Fprintf(&b, "### %s (FAILED)\nError: %s\n\n", t.Title, t.Result)
		}
	}
	b.WriteString("Synthesise the above results into a comprehensive final answer that addresses the original goal. ")
	b.WriteString("If some tasks failed, note any gaps in the result.")
	return b.String()
}

// buildTaskPrompt renders the worker prompt for a task, injecting any
// messages addressed to the assignee from the bus. Migrated from
// orchestrator.ts buildTaskPrompt.
func buildTaskPrompt(t *coordkit.Task, bus *coordkit.MessageBus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: %s\n\n%s", t.Title, t.Desc)
	if bus != nil && t.Assignee != "" {
		if msgs := bus.GetAll(t.Assignee); len(msgs) > 0 {
			b.WriteString("\n\n## Messages from team members\n")
			for _, m := range msgs {
				fmt.Fprintf(&b, "- **%s**: %s\n", m.From, m.Content)
			}
		}
	}
	return b.String()
}

func allTasksSucceeded(tasks []*coordkit.Task) bool {
	for _, t := range tasks {
		if t.Status != coordkit.TaskCompleted {
			return false
		}
	}
	return true
}
