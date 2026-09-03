package multiagent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"cyberstrike-ai/internal/multiagent/coordkit"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// fakeCoordinatorAgent is a one-shot adk.Agent used to drive the coordinator
// runner in tests without a real LLM. It returns a canned assistant message
// for every Run call and records the prompts it received so tests can assert
// on the decomposition and synthesis prompts.
type fakeCoordinatorAgent struct {
	mu       sync.Mutex
	prompts  []string
	response string
}

func newFakeCoordinatorAgent(response string) *fakeCoordinatorAgent {
	return &fakeCoordinatorAgent{response: response}
}

func (f *fakeCoordinatorAgent) Name(context.Context) string { return "fake-coordinator" }
func (f *fakeCoordinatorAgent) Description(context.Context) string {
	return "fake coordinator for tests"
}

func (f *fakeCoordinatorAgent) Run(_ context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	f.mu.Lock()
	prompt := ""
	if len(input.Messages) > 0 {
		prompt = input.Messages[len(input.Messages)-1].Content
	}
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()

	msg := schema.AssistantMessage(f.response, nil)
	ev := &adk.AgentEvent{
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				Message: msg,
			},
		},
	}
	// Mirror adk's mockAgentForTool pattern: a paired iterator/generator with
	// a goroutine that sends the single event then closes.
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
		generator.Send(ev)
	}()
	return iterator
}

func (f *fakeCoordinatorAgent) Resume(context.Context, *adk.ResumeInfo, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return nil
}

func (f *fakeCoordinatorAgent) promptsReceived() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.prompts))
	copy(out, f.prompts)
	return out
}

// fakeWorkerAgent is a one-shot adk.Agent used as the worker behind
// CoordinatorWorkerFactory. It returns a deterministic string per assignee so
// tests can assert that the right worker ran and its output reached synthesis.
type fakeWorkerAgent struct {
	assignee string
	out      string
}

func newFakeWorkerAgent(assignee, out string) *fakeWorkerAgent {
	return &fakeWorkerAgent{assignee: assignee, out: out}
}

func (w *fakeWorkerAgent) Name(context.Context) string        { return w.assignee }
func (w *fakeWorkerAgent) Description(context.Context) string { return "fake worker " + w.assignee }

func (w *fakeWorkerAgent) Run(context.Context, *adk.AgentInput, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	msg := schema.AssistantMessage(w.out, nil)
	ev := &adk.AgentEvent{
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				Message: msg,
			},
		},
	}
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
		generator.Send(ev)
	}()
	return iterator
}

func (w *fakeWorkerAgent) Resume(context.Context, *adk.ResumeInfo, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return nil
}

// TestCoordinatorRunner_FullDecomposeDispatchSynthesize drives the whole
// runTeam loop with fake agents: the coordinator returns a 4-task diamond DAG,
// four workers run (Recon, then Scan+Enum in parallel, then Report), and the
// final synthesis text is returned. This is the K6 E2E for the coordinator
// primitive without touching real LLMs (paid-API red line).
func TestCoordinatorRunner_FullDecomposeDispatchSynthesize(t *testing.T) {
	// Coordinator returns a diamond: Recon -> {Scan, Enum} -> Report.
	decompJSON := "```json\n" +
		`[{"title":"Recon","description":"port scan","assignee":"scanner","dependsOn":[]},` +
		`{"title":"Scan","description":"vuln scan","assignee":"scanner","dependsOn":["Recon"]},` +
		`{"title":"Enum","description":"enum users","assignee":"enumerator","dependsOn":["Recon"]},` +
		`{"title":"Report","description":"writeup","assignee":"writer","dependsOn":["Scan","Enum"]}]` +
		"\n```"
	coordinatorAgent := newFakeCoordinatorAgent(decompJSON)

	workerOutputs := map[string]string{
		"scanner":    "[recon done][scan done]",
		"enumerator": "[enum done]",
		"writer":     "[report done]",
	}
	runner, err := NewCoordinatorRunner(CoordinatorConfig{
		AgentFactory: func(ctx context.Context, instruction string) (adk.Agent, error) {
			return coordinatorAgent, nil
		},
		WorkerFactory: func(ctx context.Context, assignee string) (adk.Agent, error) {
			out, ok := workerOutputs[assignee]
			if !ok {
				t.Fatalf("unknown worker assignee in test: %q", assignee)
			}
			return newFakeWorkerAgent(assignee, out), nil
		},
		KnownAgents: []string{"scanner", "enumerator", "writer"},
	})
	if err != nil {
		t.Fatalf("NewCoordinatorRunner: %v", err)
	}

	res, err := runner.Run(context.Background(), "conduct a pentest")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got failed tasks")
	}
	if len(res.Tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(res.Tasks))
	}
	if res.Response != decompJSON {
		t.Errorf("synthesis text should be coordinator's canned response, got %q", res.Response)
	}
	if res.FallbackUsed {
		t.Error("fallback should not be used when decomposition is valid JSON")
	}

	// All tasks must be completed.
	for _, task := range res.Tasks {
		if task.Status != coordkit.TaskCompleted {
			t.Errorf("task %q status = %v, want completed", task.Title, task.Status)
		}
	}

	// The coordinator was invoked twice: decompose + synthesis.
	prompts := coordinatorAgent.promptsReceived()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 coordinator calls (decompose + synth), got %d", len(prompts))
	}
	if !strings.Contains(prompts[0], "conduct a pentest") {
		t.Errorf("decompose prompt should contain goal: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "Report") {
		t.Errorf("synthesis prompt should reference completed tasks: %q", prompts[1])
	}
}

// TestCoordinatorRunner_FallbackOnUnparsableDecomposition verifies the
// fallback path: when the coordinator returns non-JSON, every known agent
// gets one task with the goal as description. Mirrors orchestrator.ts's
// fallback branch.
func TestCoordinatorRunner_FallbackOnUnparsableDecomposition(t *testing.T) {
	coordinatorAgent := newFakeCoordinatorAgent("sorry, I cannot decompose that")
	workerOutputs := map[string]string{
		"a1": "a1-out",
		"a2": "a2-out",
	}
	runner, err := NewCoordinatorRunner(CoordinatorConfig{
		AgentFactory: func(ctx context.Context, instruction string) (adk.Agent, error) {
			return coordinatorAgent, nil
		},
		WorkerFactory: func(ctx context.Context, assignee string) (adk.Agent, error) {
			out, ok := workerOutputs[assignee]
			if !ok {
				t.Fatalf("unknown worker %q", assignee)
			}
			return newFakeWorkerAgent(assignee, out), nil
		},
		KnownAgents: []string{"a1", "a2"},
	})
	if err != nil {
		t.Fatalf("NewCoordinatorRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FallbackUsed {
		t.Error("expected fallback to be used")
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("expected 2 fallback tasks, got %d", len(res.Tasks))
	}
	for _, task := range res.Tasks {
		if task.Desc != "do the thing" {
			t.Errorf("fallback task desc should be goal, got %q", task.Desc)
		}
	}
}

// TestCoordinatorRunner_WorkerFailurePropagates verifies that a worker that
// fails to build (WorkerFactory error) marks its task failed and the run
// reports success=false, but other independent tasks still complete.
func TestCoordinatorRunner_WorkerFailurePropagates(t *testing.T) {
	decompJSON := "```json\n" +
		`[{"title":"Good","description":"ok","assignee":"good","dependsOn":[]},` +
		`{"title":"Bad","description":"boom","assignee":"bad","dependsOn":[]}]` +
		"\n```"
	coordinatorAgent := newFakeCoordinatorAgent(decompJSON)
	runner, err := NewCoordinatorRunner(CoordinatorConfig{
		AgentFactory: func(ctx context.Context, instruction string) (adk.Agent, error) {
			return coordinatorAgent, nil
		},
		WorkerFactory: func(ctx context.Context, assignee string) (adk.Agent, error) {
			if assignee == "bad" {
				return nil, errWorkerBuild
			}
			return newFakeWorkerAgent(assignee, "good-out"), nil
		},
		KnownAgents: []string{"good", "bad"},
	})
	if err != nil {
		t.Fatalf("NewCoordinatorRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), "goal")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Success {
		t.Error("expected overall success=false because Bad failed")
	}
	var good, bad *coordkit.Task
	for _, task := range res.Tasks {
		switch task.Title {
		case "Good":
			good = task
		case "Bad":
			bad = task
		}
	}
	if good == nil || good.Status != coordkit.TaskCompleted {
		t.Errorf("Good should be completed: %+v", good)
	}
	if bad == nil || bad.Status != coordkit.TaskFailed {
		t.Errorf("Bad should be failed: %+v", bad)
	}
	if bad != nil && !strings.Contains(bad.Result, "build worker") {
		t.Errorf("Bad result should mention build worker: %q", bad.Result)
	}
}

var errWorkerBuild = errWorker("build failed")

type errWorker string

func (e errWorker) Error() string { return string(e) }
