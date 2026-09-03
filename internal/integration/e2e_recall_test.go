// Package integration_test 验证 microagent + eventstream + promptassembly 三特性
// 协同工作的端到端链路（移植自 OpenHands 的 microagent → RecallAction → RecallObservation
// → prompt assembly 链路）。
//
// 链路：用户消息 → microagent.Registry.Retrieve 命中 → 发布 RecallAction 到 EventStream →
// Memory 订阅者消费 → 产出 RecallObservation（cause 链）→ promptassembly.Manager 渲染
// 含 microagent 内容的 prompt 块。
//
// 本测试独立，不依赖 CyberStrikeAI 现有 handler/agent 代码（避免被其他会话回滚影响）。
package integration_test

import (
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/eventstream"
	"cyberstrike-ai/internal/microagent"
	"cyberstrike-ai/internal/promptassembly"
)

// TestE2E_MicroagentRecallToPromptAssembly 端到端：用户消息触发 microagent 检索，
// 经 EventStream（cause 链）产出 RecallObservation，最终被 promptassembly 渲染为 prompt 块。
func TestE2E_MicroagentRecallToPromptAssembly(t *testing.T) {
	// === 准备 microagent 注册表 ===
	r := microagent.NewRegistry()
	// repo microagent（always-on）
	r.LoadLayer(writeTempMicroagents(t, map[string]string{
		"repo-conventions.md": "---\nname: repo-conventions\n---\n本项目禁止硬编码密钥。\n",
	}))
	// knowledge microagent（关键词触发）
	r.LoadLayer(writeTempMicroagents(t, map[string]string{
		"sqli.md": "---\nname: sqli\ntriggers: [sqli, sql injection]\n---\nSQLi 利用优先用 union 语法。\n",
		"xss.md":  "---\nname: xss\ntriggers: [xss]\n---\nXSS 用 svg onload。\n",
	}))
	repoCnt, knowledgeCnt := r.Stats()
	if repoCnt != 1 || knowledgeCnt != 2 {
		t.Fatalf("加载后应 repo=1 knowledge=2，got repo=%d knowledge=%d", repoCnt, knowledgeCnt)
	}

	// === 准备 EventStream + promptassembly Manager ===
	stream := eventstream.NewEventStream(eventstream.NewMemoryStore())
	defer stream.Close()
	paMgr := promptassembly.NewManager()

	// === Memory 订阅者：消费 RecallAction → 产出 RecallObservation ===
	// 移植自 openhands/memory/memory.py:42-138 Memory.on_event。
	var recalledObservation *eventstream.RecallObservation
	_, err := stream.Subscribe(eventstream.SubscriberMemory, "memory", 16, func(ev eventstream.Event) {
		if ev.EventType() != "recall_action" {
			return
		}
		action, ok := ev.(*eventstream.RecallAction)
		if !ok {
			return
		}
		obs := &eventstream.RecallObservation{
			RecallType: action.RecallType,
		}
		if action.RecallType == eventstream.RecallTypeWorkspaceContext {
			// always-on repo 内容注入
			obs.RepoInstructions = r.RepoContent()
			obs.WorkingDir = "/test-workspace"
			obs.Date = promptassembly.DefaultDate()
		} else if action.RecallType == eventstream.RecallTypeKnowledge {
			// 关键词检索注入
			hits := r.Retrieve("conv-e2e", action.Query)
			for _, h := range hits {
				obs.MicroagentKnowledge = append(obs.MicroagentKnowledge, eventstream.MicroagentKnowledge{
					Name: h.Name, Trigger: h.Trigger, Content: h.Content,
				})
			}
		}
		// 发布 Observation，cause=action.ID 建立 cause 链。
		_, _ = stream.AddEvent(obs, eventstream.SourceEnvironment, action.ID())
	})
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	// === 模拟首条用户消息：触发 WORKSPACE_CONTEXT recall ===
	firstMsgAction := &eventstream.RecallAction{
		RecallType: eventstream.RecallTypeWorkspaceContext,
		Query:      "帮我测一下这个网站",
	}
	actionID, err := stream.AddEvent(firstMsgAction, eventstream.SourceUser, 0)
	if err != nil {
		t.Fatalf("AddEvent recall_action: %v", err)
	}

	// === 模拟后续用户消息：触发 KNOWLEDGE recall（关键词 sqli）===
	knowledgeAction := &eventstream.RecallAction{
		RecallType: eventstream.RecallTypeKnowledge,
		Query:      "我发现一个 sqli 注入点",
	}
	knowledgeActionID, err := stream.AddEvent(knowledgeAction, eventstream.SourceUser, 0)
	if err != nil {
		t.Fatalf("AddEvent knowledge recall: %v", err)
	}

	// 等待 Memory 订阅者处理完两个 RecallObservation
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// 拉取 WORKSPACE_CONTEXT 分支产出的 RecallObservation（cause=actionID），
		// 它包含 RepoInstructions + WorkingDir（always-on repo 注入）。
		workspaceObs := searchObservations(stream, actionID)
		if len(workspaceObs) > 0 {
			// 检查 KNOWLEDGE 分支是否也已产出（cause=knowledgeActionID）
			knowledgeObs := searchObservations(stream, knowledgeActionID)
			if len(knowledgeObs) > 0 {
				recalledObservation = knowledgeObs[0]
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recalledObservation == nil {
		t.Fatal("Memory 订阅者未产出 RecallObservation（cause 链断裂）")
	}

	// workspaceObs 供后续断言（cause=actionID，含 RepoInstructions）。
	workspaceObs := searchObservations(stream, actionID)
	if len(workspaceObs) == 0 {
		t.Fatal("首个 WORKSPACE_CONTEXT recall 未产出 RecallObservation")
	}

	// === 用 promptassembly 渲染：把 RecallObservation 字段映射到 Context ===
	// 移植自 openhands/memory/conversation_memory.py:540-638 struct 重建。
	// 用 WORKSPACE_CONTEXT observation 作为 prompt 块来源（含 repo 指令 + 工作目录）。
	ctx := promptassembly.Context{
		RuntimeInfo: promptassembly.RuntimeInfo{
			Date:       workspaceObs[0].Date,
			WorkingDir: workspaceObs[0].WorkingDir,
		},
		RepoInstructions: workspaceObs[0].RepoInstructions,
	}
	// microagent 触发块（KNOWLEDGE 分支命中，来自 knowledgeActionID 的 observation）
	var mk []promptassembly.MicroagentKnowledge
	for _, k := range recalledObservation.MicroagentKnowledge {
		mk = append(mk, promptassembly.MicroagentKnowledge{Name: k.Name, Trigger: k.Trigger, Content: k.Content})
	}
	promptBlock := paMgr.Render(ctx)
	microagentBlock := paMgr.RenderMicroagentInfo(mk)

	// === 断言：渲染结果含 always-on repo 内容 + sqli microagent 内容 ===
	if !contains(promptBlock, "本项目禁止硬编码密钥") {
		t.Errorf("prompt 块应含 repo microagent 内容，got:\n%s", promptBlock)
	}
	if !contains(promptBlock, "/test-workspace") {
		t.Errorf("prompt 块应含 WorkingDir，got:\n%s", promptBlock)
	}
	if !contains(microagentBlock, "SQLi 利用优先用 union 语法") {
		t.Errorf("microagent 块应含 sqli 内容，got:\n%s", microagentBlock)
	}
	if !contains(microagentBlock, "sqli") {
		t.Errorf("microagent 块应含触发关键词 sqli，got:\n%s", microagentBlock)
	}
	// 不应含 xss（消息里没提 xss，不应误触发）
	if contains(microagentBlock, "svg onload") {
		t.Errorf("xss microagent 不应被触发（消息无 xss 关键词），got:\n%s", microagentBlock)
	}

	// === 验证 cause 链：RecallObservation.Cause == actionID 或 knowledgeActionID ===
	if recalledObservation.Cause() != knowledgeActionID {
		t.Errorf("cause 链断裂：期望 Cause=%d，got %d", knowledgeActionID, recalledObservation.Cause())
	}

	// === 验证首个 WORKSPACE_CONTEXT recall 也产出了 observation（cause=actionID）===
	if workspaceObs[0].Cause() != actionID {
		t.Errorf("workspace observation cause 应为 %d，got %d", actionID, workspaceObs[0].Cause())
	}
	if workspaceObs[0].WorkingDir != "/test-workspace" {
		t.Errorf("workspace observation 应含 WorkingDir，got %q", workspaceObs[0].WorkingDir)
	}
}

// searchObservations 从 stream 的 store 中检索 recall_observation 且 cause==expected 的观测。
func searchObservations(stream *eventstream.EventStream, expectedCause int64) []*eventstream.RecallObservation {
	var result []*eventstream.RecallObservation
	for id := int64(1); id <= stream.LatestEventID(); id++ {
		ev, ok := stream.GetEventByID(id)
		if !ok {
			continue
		}
		if ev.EventType() != "recall_observation" {
			continue
		}
		if ev.Cause() != expectedCause {
			continue
		}
		if obs, ok := ev.(*eventstream.RecallObservation); ok {
			result = append(result, obs)
		}
	}
	return result
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
