package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// runDelayNode 延时节点：阻塞 duration_ms 毫秒后继续。用于节奏控制/等待外部系统收敛。
// duration_ms 可来自 node.Config["duration_ms"]（字符串/数字）或 node.Data["duration_ms"]。
// 单独抽到 nodes_control.go 便于在 nodes.go 的 switch 里保持轻量。
func runDelayNode(ctx context.Context, _ RunArgs, node graphNode, _ *WorkflowLocalState) (map[string]any, bool, string, string) {
	ms := readDurationMillis(node.Config)
	if ms <= 0 {
		errText := "延时节点缺少有效的 duration_ms（必须 > 0）"
		return outputMap(envelope("delay", node.ID, node.Type, "failed", ""), map[string]any{"duration_ms": 0, "error": errText}), false, "failed", errText
	}
	// 用 select 监听 ctx.Done()，让取消/超时能尽早中断 sleep；非阻塞路径返回 completed。
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-ctx.Done():
		errText := "延时节点被取消"
		return outputMap(envelope("delay", node.ID, node.Type, "cancelled", ""), map[string]any{"duration_ms": ms, "error": errText}), false, "failed", errText
	}
	return outputMap(envelope("delay", node.ID, node.Type, "completed", ""), map[string]any{"duration_ms": ms}), true, "completed", ""
}

// readDurationMillis 从节点 config 读 duration_ms，兼容 int/float/string。无效返回 0。
func readDurationMillis(cfg map[string]any) int64 {
	if cfg == nil {
		return 0
	}
	raw, ok := cfg["duration_ms"]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		if err != nil || n <= 0 {
			return 0
		}
		return n
	default:
		s := strings.TrimSpace(fmt.Sprint(raw))
		var n int64
		_, err := fmt.Sscanf(s, "%d", &n)
		if err != nil || n <= 0 {
			return 0
		}
		return n
	}
}

// runLoopNode 循环节点：对 items 数组逐项执行一段内联 lambda（source_binding + 模板），
// 聚合为 results 数组。降级方案：不走 Eino 子图，直接同步执行（保证可跑）。
// 配置：
//   - items: 静态数组（Config["items"]），或来自 items_binding 的运行时数组
//   - item_key: 把当前项写入 state.Inputs 的哪个键（默认 loop_item）
//   - body_instruction: 每一轮的指令文本（写入 state.LastOutput["loop_instruction"]）
//   - output_key: 聚合结果写入 state.Outputs 的键名
func runLoopNode(_ context.Context, args RunArgs, node graphNode, state *WorkflowLocalState) (map[string]any, bool, string, string) {
	items := resolveLoopItems(node.Config, state)
	if len(items) == 0 && args.Progress != nil {
		// binding 命中但解析为空（字段拼错/值为空）时静默 0 次循环难以察觉，显式告警。
		if _, hasBinding := parseFieldBinding(node.Config, "items_binding"); hasBinding {
			args.Progress("workflow_loop_empty", fmt.Sprintf("循环节点「%s」的 items_binding 解析为空，本轮循环执行 0 项", firstNonEmpty(node.Label, node.ID)), map[string]any{"nodeId": node.ID})
		}
	}
	itemKey := cfgString(node.Config, "item_key")
	if itemKey == "" {
		itemKey = "loop_item"
	}
	outputKey := cfgString(node.Config, "output_key")
	if outputKey == "" {
		outputKey = node.ID + "_loop"
	}
	results := make([]any, 0, len(items))
	failed := false
	failFast := joinStrategy(node) == JoinFailFast
	errText := ""
	for idx, item := range items {
		if state.Inputs == nil {
			state.Inputs = map[string]any{}
		}
		state.Inputs[itemKey] = item
		state.Inputs["loop_index"] = idx
		// 每轮把上一轮的 result 作为 previous.output，让 body 可链式引用
		bodyOut := executeLoopBody(node, state, idx, item)
		results = append(results, bodyOut)
		if isFailedNodeOutput(bodyOut) {
			failed = true
			if failFast {
				errText = cfgString(bodyOut, "error")
				if errText == "" {
					errText = "循环节点 fail_fast 在某轮失败后中止"
				}
				break
			}
		}
	}
	state.Outputs[outputKey] = results
	if args.Progress != nil {
		args.Progress("workflow_loop_step", fmt.Sprintf("循环节点「%s」执行 %d 项", firstNonEmpty(node.Label, node.ID), len(results)), map[string]any{
			"nodeId":    node.ID,
			"itemKey":   itemKey,
			"itemCount": len(results),
		})
	}
	if failed && failFast {
		return outputMap(envelope("loop", node.ID, node.Type, "failed", ""), map[string]any{"results": results, "error": errText, "output_key": outputKey}), false, "failed", errText
	}
	status := "completed"
	if failed {
		status = "completed_with_errors"
	}
	return outputMap(envelope("loop", node.ID, node.Type, status, results), map[string]any{"results": results, "output_key": outputKey, "item_count": len(results)}), true, status, ""
}

// resolveLoopItems 从静态 Config["items"] 或 items_binding 取数组。非数组返回空切片。
func resolveLoopItems(cfg map[string]any, state *WorkflowLocalState) []any {
	if cfg == nil {
		return nil
	}
	if raw, ok := cfg["items"]; ok {
		if arr, ok := raw.([]any); ok {
			return arr
		}
	}
	if b, ok := parseFieldBinding(cfg, "items_binding"); ok {
		resolved := resolveBinding(b, state)
		if arr, ok := resolved.([]any); ok {
			return arr
		}
		// 命中但非数组 → 返回单元素切片，保证循环至少跑一次（符合 "count 次 repeat" 语义）
		if resolved != nil && fmt.Sprint(resolved) != "" {
			return []any{resolved}
		}
	}
	// 兜底 count 次 repeat：Config["count"] 控制迭代次数
	if cnt := readCountFromConfig(cfg); cnt > 0 {
		out := make([]any, 0, cnt)
		for i := 0; i < cnt; i++ {
			out = append(out, i)
		}
		return out
	}
	return nil
}

func readCountFromConfig(cfg map[string]any) int {
	if cfg == nil {
		return 0
	}
	raw, ok := cfg["count"]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case string:
		var n int
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		if err != nil || n <= 0 {
			return 0
		}
		return n
	default:
		return 0
	}
}

// executeLoopBody 执行单轮 body（同步、不进 Eino 子图）。
// 当前实现把本轮的 body_instruction 作为 LastOutput 输出，方便下游节点引用；
// 真正的子图循环属于降级方案之外的后续增强项。
func executeLoopBody(node graphNode, state *WorkflowLocalState, idx int, item any) map[string]any {
	instruction := strings.TrimSpace(cfgString(node.Config, "body_instruction"))
	if instruction == "" {
		instruction = fmt.Sprintf("第 %d 轮迭代，输入：%v", idx+1, item)
	}
	out := outputMap(envelope("loop_step", node.ID, node.Type, "completed", instruction), map[string]any{
		"index":       idx,
		"item":        item,
		"instruction": instruction,
	})
	state.LastOutput = out
	return out
}

// runParallelNode 并行节点：并发执行多个 branches（每条 branch 是一段内联 lambda），
// 按 join_strategy 汇聚。降级方案：用 errgroup/sync.WaitGroup 在 lambda 内同步执行，
// 不编译成 Eino 并行子图——保证可跑，等后续增强项再接 Eino 并行原语。
// 配置：
//   - branches: Config["branches"]，每项是 map[string]any，含 instruction / value 字段
//   - join_strategy: 见 join.go（all_merge/last_by_canvas/first_non_empty/fail_fast）
//   - output_key: 聚合结果写入 state.Outputs 的键名
func runParallelNode(_ context.Context, args RunArgs, node graphNode, state *WorkflowLocalState) (map[string]any, bool, string, string) {
	branches := readParallelBranches(node.Config)
	strategy := joinStrategy(node)
	outputKey := cfgString(node.Config, "output_key")
	if outputKey == "" {
		outputKey = node.ID + "_parallel"
	}
	if len(branches) == 0 {
		errText := "并行节点缺少 branches 配置"
		return outputMap(envelope("parallel", node.ID, node.Type, "failed", ""), map[string]any{"error": errText, "output_key": outputKey}), false, "failed", errText
	}
	results := make([]map[string]any, len(branches))
	errs := make([]string, len(branches))
	var wg sync.WaitGroup
	var mu sync.Mutex
	firstErr := ""
	for i, br := range branches {
		wg.Add(1)
		go func(idx int, branch map[string]any) {
			defer wg.Done()
			out := executeParallelBranch(node, state, idx, branch)
			results[idx] = out
			if isFailedNodeOutput(out) {
				errs[idx] = cfgString(out, "error")
				mu.Lock()
				if firstErr == "" {
					firstErr = errs[idx]
				}
				mu.Unlock()
			}
		}(i, br)
	}
	wg.Wait()
	merged := mergeUpstreamOutputs(strategy, results)
	state.Outputs[outputKey] = merged
	if args.Progress != nil {
		args.Progress("workflow_parallel_done", fmt.Sprintf("并行节点「%s」执行 %d 分支", firstNonEmpty(node.Label, node.ID), len(branches)), map[string]any{
			"nodeId":     node.ID,
			"branchCnt":  len(branches),
			"strategy":   strategy,
			"outputKey":  outputKey,
			"firstError": firstErr,
		})
	}
	if firstErr != "" && strategy == JoinFailFast {
		return outputMap(envelope("parallel", node.ID, node.Type, "failed", ""), map[string]any{"results": results, "error": firstErr, "output_key": outputKey, "strategy": strategy}), false, "failed", firstErr
	}
	status := "completed"
	if firstErr != "" {
		status = "completed_with_errors"
	}
	return outputMap(envelope("parallel", node.ID, node.Type, status, merged), map[string]any{"results": results, "output_key": outputKey, "strategy": strategy, "branch_count": len(branches)}), true, status, ""
}

// readParallelBranches 从 Config["branches"] 取数组；容忍 nil / 非数组返回 nil。
func readParallelBranches(cfg map[string]any) []map[string]any {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["branches"]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
			continue
		}
		// 兜底：标量值包成 {"value": x}
		out = append(out, map[string]any{"value": item})
	}
	return out
}

// executeParallelBranch 执行单条 branch（同步 lambda）。当前实现把 branch 的 instruction/value
// 包装成 LastOutput 返回；真正并行子图属于后续增强项。
func executeParallelBranch(node graphNode, _ *WorkflowLocalState, idx int, branch map[string]any) map[string]any {
	instruction := strings.TrimSpace(cfgString(branch, "instruction"))
	value := branch["value"]
	if instruction == "" && value == nil {
		instruction = fmt.Sprintf("并行分支 %d", idx+1)
	}
	out := outputMap(envelope("parallel_branch", node.ID, node.Type, "completed", value), map[string]any{
		"branch_index": idx,
		"instruction":  instruction,
		"value":        value,
	})
	return out
}

// sortStrings 工具：给 loop/parallel 元数据 key 排序，避免 map 迭代顺序导致测试不稳定。
func sortStrings(in []string) []string {
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	return sorted
}
