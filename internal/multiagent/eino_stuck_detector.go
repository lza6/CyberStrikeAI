package multiagent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/securityevents"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// StuckDetector agent 卡死检测器（K9 二级防线）。
//
// 设计移植自 OpenHands/OpenSwarm StuckDetector 四阈值：
//   - sameOutputRepeat：连续 N 次输出归一化哈希相同 → agent 陷入复读循环。
//   - sameErrorRepeat：连续 N 次相同错误哈希 → 重试无进展。
//   - revisionLoop：连续 N 次工具调用参数哈希相同 → 在同一目标上反复操作（revision loop）。
//   - monologue：连续 N 轮无工具调用、无 user 输入 → agent 自言自语无进展。
//
// 与 J7 TurnToolCallLimiter 形成二级防线：
//   - TurnLimiter 防"单轮内退化生成排队大量工具调用"（一级，限流）。
//   - StuckDetector 防"跨轮退化循环"（二级，检测+升级通知）。
//
// 触发后调 securityevents.PublishAgentStuck → blackboard → reactions 引擎
// （agent-stuck 规则默认 notify urgent），与 HIGH_IMPACT/scope 拦截同款通道。
//
// 误报防护（CRITICAL）：
//   - recon/enumeration 类工具（nmap/masscan/subfinder/amass/nikto/dirb/gobuster/ffuf/hydra/enum4linux/legion/recon-ng/theHarvester 等）白名单豁免 sameOutputRepeat：这类工具正常情况下对同一目标重复执行产出相同（如端口扫描结果稳定），非卡死。
//   - 输出哈希前归一化：剥离时间戳（[12:34:56] / 2026-09-04T... / 09/04 12:34）、进度行（[###] 50% / Progress: 80%）、行号前缀，仅保留语义内容，防"时间不同导致哈希不同"漏报与"进度不同导致哈希不同"误报。
//   - 同一 conversationID 内同一阈值触发后冷却 cooldown（默认 5m），防 reactions 重复升级通知刷屏。
type StuckDetector struct {
	mu sync.Mutex

	// 四阈值（与参考项目默认值对齐）。
	sameOutputRepeat int // 默认 3
	sameErrorRepeat  int // 默认 2
	revisionLoop     int // 默认 4
	monologue        int // 默认 6

	// 冷却：同一 conversationID 同一阈值触发后冷却内不再发，防 reactions 重复升级。
	cooldown time.Duration

	// 状态（per conversationID）。
	lastOutputHash   map[string]string // convID → 最近一次输出归一化哈希
	outputRepeat     map[string]int    // convID → 连续相同输出次数
	lastErrorHash    map[string]string // convID → 最近一次错误归一化哈希
	errorRepeat      map[string]int    // convID → 连续相同错误次数
	toolArgRepeat    map[string]int    // convID → 连续相同工具参数次数
	monologueCount   map[string]int    // convID → 连续无工具调用轮数
	lastFireByKind   map[string]map[string]time.Time // convID → kind → 上次触发时间

	// 工具结果观察缓冲（用于 revisionLoop 参数哈希计算前的工具调用采集）。
	lastToolCall map[string]string // convID → 最近一次工具调用签名（name+args 归一化）

	// 可选 logger，触发时记 Warn 供运维排查。
	logger *zap.Logger
}

// StuckDetectorConfig 配置 StuckDetector。零值字段走 DefaultStuckDetectorConfig。
type StuckDetectorConfig struct {
	SameOutputRepeat int
	SameErrorRepeat  int
	RevisionLoop     int
	Monologue        int
	Cooldown         time.Duration
	Logger           *zap.Logger
}

// DefaultStuckDetectorConfig 默认阈值（与参考项目 OpenHands StuckDetector 对齐）。
func DefaultStuckDetectorConfig() StuckDetectorConfig {
	return StuckDetectorConfig{
		SameOutputRepeat: 3,
		SameErrorRepeat:  2,
		RevisionLoop:     4,
		Monologue:        6,
		Cooldown:         5 * time.Minute,
	}
}

// NewStuckDetector 构造检测器。nil cfg 走默认配置。
func NewStuckDetector(cfg StuckDetectorConfig) *StuckDetector {
	if cfg.SameOutputRepeat <= 0 {
		cfg.SameOutputRepeat = 3
	}
	if cfg.SameErrorRepeat <= 0 {
		cfg.SameErrorRepeat = 2
	}
	if cfg.RevisionLoop <= 0 {
		cfg.RevisionLoop = 4
	}
	if cfg.Monologue <= 0 {
		cfg.Monologue = 6
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 5 * time.Minute
	}
	return &StuckDetector{
		sameOutputRepeat: cfg.SameOutputRepeat,
		sameErrorRepeat:  cfg.SameErrorRepeat,
		revisionLoop:     cfg.RevisionLoop,
		monologue:        cfg.Monologue,
		cooldown:         cfg.Cooldown,
		logger:           cfg.Logger,
		lastOutputHash:   make(map[string]string),
		outputRepeat:     make(map[string]int),
		lastErrorHash:    make(map[string]string),
		errorRepeat:      make(map[string]int),
		toolArgRepeat:    make(map[string]int),
		monologueCount:   make(map[string]int),
		lastFireByKind:   make(map[string]map[string]time.Time),
		lastToolCall:     make(map[string]string),
	}
}

// stuckReconWhitelist recon/enumeration 类工具白名单（豁免 sameOutputRepeat）。
// 这类工具正常情况下对同一目标重复执行产出相同（端口扫描结果稳定），非卡死。
var stuckReconWhitelist = map[string]bool{
	"nmap": true, "masscan": true, "subfinder": true, "amass": true,
	"nikto": true, "dirb": true, "gobuster": true, "ffuf": true,
	"hydra": true, "enum4linux": true, "legion": true,
	"recon-ng": true, "reconng": true, "theharvester": true, "the_harvester": true,
	"shodan": true, "censys": true, "wpscan": true,
	"nuclei": true, "naabu": true, "httpx": true,
	"rustscan": true, "fierce": true,
}

// isReconTool 判断工具名是否在 recon 白名单（trim + lower 对齐）。
func isReconTool(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false
	}
	if stuckReconWhitelist[name] {
		return true
	}
	// 前缀匹配 execute 命令中的工具名（如 "nmap -sV target"）。
	for wl := range stuckReconWhitelist {
		wl = strings.TrimSpace(wl)
		if wl != "" && strings.HasPrefix(name, wl) {
			return true
		}
	}
	return false
}

// stuckTimestampRe 时间戳正则（[12:34:56] / 2026-09-04T12:34:56 / 09/04 12:34）。
var stuckTimestampRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
var stuckTimeOnlyRe = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}\b`)
var stuckDateOnlyRe = regexp.MustCompile(`\b\d{2}/\d{2}\s+\d{2}:\d{2}\b`)
var stuckISODateRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)

// stuckProgressRe 进度行正则（[###] 50% / Progress: 80% / 100/200）。
var stuckProgressRe = regexp.MustCompile(`(?i)\[#+\]\s*\d+%`)
var stuckProgressWordRe = regexp.MustCompile(`(?i)progress[:\s]+\d+%`)
var stuckCounterRe = regexp.MustCompile(`\b\d+/\d+\b`)

// normalizeStuckOutput 归一化输出：剥离时间戳/进度行/行号前缀/多余空白。
// 目的：让"时间不同但内容相同"的输出哈希相同（避免漏报），
// 同时让"进度不同但内容相同"的输出哈希相同（避免误报）。
func normalizeStuckOutput(s string) string {
	if s == "" {
		return ""
	}
	// 剥离时间戳。
	s = stuckTimestampRe.ReplaceAllString(s, "")
	s = stuckTimeOnlyRe.ReplaceAllString(s, "")
	s = stuckDateOnlyRe.ReplaceAllString(s, "")
	s = stuckISODateRe.ReplaceAllString(s, "")
	// 剥离进度行。
	s = stuckProgressRe.ReplaceAllString(s, "")
	s = stuckProgressWordRe.ReplaceAllString(s, "")
	s = stuckCounterRe.ReplaceAllString(s, "")
	// 按行处理后去空白行 + trim 每行。
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		// 去行号前缀（如 "1: ", "  2. "）。
		ln = stuckLineNumberRe.ReplaceAllString(ln, "")
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, "\n")
}

// stuckLineNumberRe 行号前缀正则（行首数字+点/冒号）。
var stuckLineNumberRe = regexp.MustCompile(`^\s*\d+\s*[:.)]\s*`)

// hashStuckOutput 计算归一化输出的稳定哈希（FNV-1a，无需 crypto，足够去重）。
func hashStuckOutput(s string) string {
	normalized := normalizeStuckOutput(s)
	if normalized == "" {
		return ""
	}
	// FNV-1a 64bit。
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(normalized); i++ {
		h ^= uint64(normalized[i])
		h *= prime
	}
	return fmt.Sprintf("fnv:%x", h)
}

// StuckEvent 是 StuckDetector 触发的事件（供测试与调用方观测）。
type StuckEvent struct {
	ConversationID string
	Kind            string // "same-output-repeat" / "same-error-repeat" / "revision-loop" / "monologue"
	Count           int
	Threshold       int
	Reason          string
}

// ObserveAssistantOutput 观察一次助手输出（materialized 或 stream complete）。
//   - conversationID 会话 ID
//   - content 助手正文（已 trim）
//   - toolCalls 本轮工具调用（非空则同时走 revisionLoop 检测；空则计 monologue）
//
// 触发任一阈值时返回 StuckEvent，调用方（eino_adk_run_loop）据此调
// securityevents.PublishAgentStuck。冷却内不重复触发。
func (d *StuckDetector) ObserveAssistantOutput(conversationID, content string, toolCalls []schema.ToolCall) *StuckEvent {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	convID := strings.TrimSpace(conversationID)
	if convID == "" {
		return nil
	}

	var event *StuckEvent

	// revisionLoop：工具调用参数哈希连续相同。
	if len(toolCalls) > 0 {
		// 取首个工具调用签名（与参考项目 revision loop 检测对齐：单工具调用循环最典型）。
		tc := toolCalls[0]
		signature := toolCallStuckSignature(tc)
		if signature != "" {
			if d.lastToolCall[convID] == signature {
				d.toolArgRepeat[convID]++
			} else {
				d.toolArgRepeat[convID] = 1
				d.lastToolCall[convID] = signature
			}
			if d.toolArgRepeat[convID] >= d.revisionLoop && !isReconTool(tc.Function.Name) {
				event = &StuckEvent{
					ConversationID: convID,
					Kind:           "revision-loop",
					Count:          d.toolArgRepeat[convID],
					Threshold:      d.revisionLoop,
					Reason:         fmt.Sprintf("revision-loop:%d", d.toolArgRepeat[convID]),
				}
			}
		}
		// 本轮有工具调用 → 重置 monologue 计数。
		d.monologueCount[convID] = 0
	} else {
		// 本轮无工具调用 → monologue 计数 +1。
		d.monologueCount[convID]++
		if d.monologueCount[convID] >= d.monologue {
			event = &StuckEvent{
				ConversationID: convID,
				Kind:           "monologue",
				Count:          d.monologueCount[convID],
				Threshold:      d.monologue,
				Reason:         fmt.Sprintf("monologue:%d", d.monologueCount[convID]),
			}
		}
	}

	// sameOutputRepeat：输出归一化哈希连续相同（recon 工具白名单豁免）。
	// 判定：仅当本轮工具调用为空或工具不在 recon 白名单时才计 sameOutputRepeat。
	outputHash := hashStuckOutput(content)
	if outputHash != "" {
		if d.lastOutputHash[convID] == outputHash {
			d.outputRepeat[convID]++
		} else {
			d.outputRepeat[convID] = 1
			d.lastOutputHash[convID] = outputHash
		}
		exempt := false
		if len(toolCalls) > 0 && isReconTool(toolCalls[0].Function.Name) {
			exempt = true
		}
		if !exempt && d.outputRepeat[convID] >= d.sameOutputRepeat && event == nil {
			event = &StuckEvent{
				ConversationID: convID,
				Kind:           "same-output-repeat",
				Count:          d.outputRepeat[convID],
				Threshold:      d.sameOutputRepeat,
				Reason:         fmt.Sprintf("same-output-repeat:%d", d.outputRepeat[convID]),
			}
		}
	}

	if event == nil {
		return nil
	}

	// 冷却：同一 convID 同一 kind 在 cooldown 内不重复触发。
	if d.lastFireByKind[convID] == nil {
		d.lastFireByKind[convID] = make(map[string]time.Time)
	}
	if last, ok := d.lastFireByKind[convID][event.Kind]; ok {
		if time.Since(last) < d.cooldown {
			return nil
		}
	}
	d.lastFireByKind[convID][event.Kind] = time.Now()

	if d.logger != nil {
		d.logger.Warn("stuck detector triggered",
			zap.String("conversationId", convID),
			zap.String("kind", event.Kind),
			zap.Int("count", event.Count),
			zap.Int("threshold", event.Threshold),
		)
	}
	return event
}

// ObserveToolError 观察一次工具错误。
//   - conversationID 会话 ID
//   - toolName 工具名（recon 白名单豁免）
//   - errorContent 错误正文（归一化哈希）
//
// 触发 sameErrorRepeat 阈值时返回 StuckEvent。
func (d *StuckDetector) ObserveToolError(conversationID, toolName, errorContent string) *StuckEvent {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	convID := strings.TrimSpace(conversationID)
	if convID == "" {
		return nil
	}

	// recon 白名单豁免（nmap 重复报错正常）。
	if isReconTool(toolName) {
		return nil
	}

	errHash := hashStuckOutput(errorContent)
	if errHash == "" {
		return nil
	}
	if d.lastErrorHash[convID] == errHash {
		d.errorRepeat[convID]++
	} else {
		d.errorRepeat[convID] = 1
		d.lastErrorHash[convID] = errHash
	}
	if d.errorRepeat[convID] < d.sameErrorRepeat {
		return nil
	}

	event := &StuckEvent{
		ConversationID: convID,
		Kind:           "same-error-repeat",
		Count:          d.errorRepeat[convID],
		Threshold:      d.sameErrorRepeat,
		Reason:         fmt.Sprintf("same-error-repeat:%d", d.errorRepeat[convID]),
	}

	// 冷却。
	if d.lastFireByKind[convID] == nil {
		d.lastFireByKind[convID] = make(map[string]time.Time)
	}
	if last, ok := d.lastFireByKind[convID][event.Kind]; ok {
		if time.Since(last) < d.cooldown {
			return nil
		}
	}
	d.lastFireByKind[convID][event.Kind] = time.Now()

	if d.logger != nil {
		d.logger.Warn("stuck detector triggered",
			zap.String("conversationId", convID),
			zap.String("kind", event.Kind),
			zap.Int("count", event.Count),
		)
	}
	return event
}

// toolCallStuckSignature 工具调用签名（name + 归一化 args）。
// 用于 revisionLoop 检测：相同工具 + 相同参数连续 N 次视为 revision loop。
func toolCallStuckSignature(tc schema.ToolCall) string {
	name := strings.ToLower(strings.TrimSpace(tc.Function.Name))
	args := strings.TrimSpace(tc.Function.Arguments)
	if name == "" {
		return ""
	}
	// args 归一化：剥离时间戳/进度（防命令中含时间戳导致签名不同）。
	args = normalizeStuckOutput(args)
	return name + "|" + args
}

// Reset 清零指定 conversationID 的所有计数（用于会话重置或测试）。
func (d *StuckDetector) Reset(conversationID string) {
	if d == nil {
		return
	}
	convID := strings.TrimSpace(conversationID)
	if convID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.lastOutputHash, convID)
	delete(d.outputRepeat, convID)
	delete(d.lastErrorHash, convID)
	delete(d.errorRepeat, convID)
	delete(d.toolArgRepeat, convID)
	delete(d.monologueCount, convID)
	delete(d.lastToolCall, convID)
	delete(d.lastFireByKind, convID)
}

// PublishStuckEvent 把 StuckEvent 经 securityevents 发布到 blackboard。
// 由 eino_adk_run_loop 在轮次边界调用（detector 返回非 nil event 时）。
// securityevents 未注入 board 时 no-op（与现有安全事件通道一致）。
func PublishStuckEvent(ev *StuckEvent) {
	if ev == nil {
		return
	}
	securityevents.PublishAgentStuck(ev.ConversationID, ev.Reason)
}

// einoStuckDetectorAdapter 把 StuckDetector 挂到 run loop 的物质化消息/工具结果事件上。
//
// 设计：StuckDetector 不直接订阅 Eino 事件流（避免引入新的回调耦合），
// 而由 eino_adk_run_loop 在以下两类节点调用：
//   - 助手消息物质化（HandleMaterialized 后）：ObserveAssistantOutput(content, toolCalls)
//   - 工具结果错误（einoToolResultIsError 为 true 时）：ObserveToolError(toolName, content)
//
// 触发的 event 由 run loop 调 PublishStuckEvent 发布到 blackboard。
type einoStuckDetectorAdapter struct {
	detector       *StuckDetector
	conversationID string
}

// newEinoStuckDetectorAdapter 构造适配器。detector 为 nil 时返回 nil（未启用）。
func newEinoStuckDetectorAdapter(detector *StuckDetector, conversationID string) *einoStuckDetectorAdapter {
	if detector == nil {
		return nil
	}
	return &einoStuckDetectorAdapter{
		detector:       detector,
		conversationID: conversationID,
	}
}

// ObserveMaterialized 处理物质化的助手消息（含工具调用）。
// 返回非 nil StuckEvent 时由调用方发布。
func (a *einoStuckDetectorAdapter) ObserveMaterialized(msg adk.Message) *StuckEvent {
	if a == nil || a.detector == nil || msg == nil {
		return nil
	}
	if msg.Role != schema.Assistant {
		return nil
	}
	content := strings.TrimSpace(msg.Content)
	calls := append([]schema.ToolCall(nil), msg.ToolCalls...)
	return a.detector.ObserveAssistantOutput(a.conversationID, content, calls)
}

// ObserveToolError 处理工具结果错误。
// 返回非 nil StuckEvent 时由调用方发布。
func (a *einoStuckDetectorAdapter) ObserveToolError(toolName, content string) *StuckEvent {
	if a == nil || a.detector == nil {
		return nil
	}
	return a.detector.ObserveToolError(a.conversationID, toolName, content)
}

// ObserveStreamComplete 处理流式完成事件（content + toolCalls）。
// 与 ObserveMaterialized 共享 StuckDetector 逻辑。
func (a *einoStuckDetectorAdapter) ObserveStreamComplete(content string, toolCalls []schema.ToolCall) *StuckEvent {
	if a == nil || a.detector == nil {
		return nil
	}
	return a.detector.ObserveAssistantOutput(a.conversationID, strings.TrimSpace(content), toolCalls)
}

// StuckDetectorAdapter Eino run loop 用的适配器接口（供 run loop 字段持有）。
// 适配器为 nil 时所有方法 no-op。
type StuckDetectorAdapter interface {
	ObserveMaterialized(msg adk.Message) *StuckEvent
	ObserveToolError(toolName, content string) *StuckEvent
	ObserveStreamComplete(content string, toolCalls []schema.ToolCall) *StuckEvent
}

// Compile-time guard: einoStuckDetectorAdapter 满足 StuckDetectorAdapter。
var _ StuckDetectorAdapter = (*einoStuckDetectorAdapter)(nil)
