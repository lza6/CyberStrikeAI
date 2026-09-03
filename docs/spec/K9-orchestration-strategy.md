# Spec: K9 — 编排策略层（orchestration strategy）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

建立多 agent 编排策略层：StuckDetector（agent 卡死检测器，二级防线）+ reactions lifecycle 状态机（SessionStatus 派生 + 转换触发）+ coordinator retry/backoff 循环。防 agent 跨轮退化循环，与 J7 TurnToolCallLimiter 形成二级防线。

## Tech Stack

- Go 1.25，`internal/multiagent` + `internal/reactions` + `internal/securityevents`
- `github.com/cloudwego/eino/adk` + `schema`（Eino 智能体编排）
- `go.uber.org/zap`
- 纯标准库（`sync`、`time`、`regexp`、`strings`）

## Commands

```bash
go vet ./internal/multiagent/ ./internal/reactions/
go test ./internal/multiagent/ ./internal/reactions/ -count=1
make test-race                          # 带 -race（StuckDetector 并发安全）
go build ./...
```

## Project Structure

```
internal/multiagent/eino_stuck_detector.go → StuckDetector（四阈值：sameOutputRepeat/sameErrorRepeat/revisionLoop/monologue）
internal/multiagent/coordinator_orchestrator.go → K9 retry/backoff 循环（最多 1 + MaxRetries 次）
internal/reactions/engine.go            → K9 lifecycle 状态机轮询（pollSessionStatus + deriveSessionStatus + publishSessionStatusFinding）
internal/reactions/engine_test.go        → lifecycle 状态转换测试
internal/reactions/e2e_test.go           → K9 lifecycle E2E（状态转换触发 reaction）
```

## Code Style

```go
// 包注释 + 设计来源 + 误报防护（匹配 internal/multiagent 风格）
// StuckDetector agent 卡死检测器（K9 二级防线）。
// 设计移植自 OpenHands/OpenSwarm StuckDetector 四阈值：
//   - sameOutputRepeat：连续 N 次输出归一化哈希相同 → 复读循环。
//   - sameErrorRepeat：连续 N 次相同错误哈希 → 重试无进展。
//   - revisionLoop：连续 N 次工具调用参数哈希相同 → revision loop。
//   - monologue：连续 N 轮无工具调用、无 user 输入 → 自言自语。
//
// 误报防护（CRITICAL）：
//   - recon/enumeration 类工具白名单豁免 sameOutputRepeat
//   - 输出哈希前归一化（剥离时间戳/进度行/行号前缀）
//   - 同一 conversationID 内冷却 cooldown（默认 5m）
type StuckDetector struct { ... }
```

## Testing Strategy

- `engine_test.go`：SessionStatus 派生（idle→running→tool_pending→hitl_pending→done→failed）；状态转换触发 reaction
- `e2e_test.go`：lifecycle E2E（全链路 Publish→consume→handleFinding→状态转换→reaction）
- StuckDetector 四阈值触发 + 误报防护（recon 白名单豁免 + 归一化 + 冷却）
- coordinator retry/backoff 循环（最多 1 + MaxRetries 次）
- 回归底线：`go test ./internal/multiagent/ ./internal/reactions/` 双路径全绿；`-race` 并发安全

## Boundaries

- **Always**：StuckDetector 与 TurnToolCallLimiter 形成二级防线（一级限流，二级检测+升级通知）；触发后调 securityevents.PublishAgentStuck → blackboard → reactions；recon/enumeration 白名单豁免；归一化 + 冷却
- **Ask first**：改四阈值默认值；改冷却时间（5m）；改 recon 白名单
- **Never**：删除 recon 白名单豁免（误报刷屏）；删除归一化（漏报+误报）；StuckDetector 触发后阻断 agent（只升级通知，不阻断）；改 lifecycle 轮询间隔为 <1s（性能）

## Success Criteria

1. StuckDetector 四阈值实现（sameOutputRepeat/sameErrorRepeat/revisionLoop/monologue）✅ done
2. 误报防护（recon 白名单豁免 + 归一化 + 冷却 5m）✅ done
3. 触发后调 securityevents.PublishAgentStuck → blackboard → reactions ✅ done
4. reactions lifecycle 状态机（SessionStatus 派生 + 转换触发）✅ done
5. coordinator retry/backoff 循环（最多 1 + MaxRetries 次）✅ done
6. StuckDetector 与 TurnToolCallLimiter 形成二级防线 ✅ done
7. `-race` 并发安全测试通过 ✅ done

## Open Questions

- StuckDetector 真实 LLM 场景下的误报率 —— 待真实 LLM evals（付费红线，当前 Mock 验证逻辑）
