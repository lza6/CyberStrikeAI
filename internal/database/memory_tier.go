package database

import (
	"math"
	"time"
)

// ============================================================================
// agentmemory 迁移项 1：4 层记忆生命周期 + 强度衰减
//
// 设计来源：agentmemory-main/src/functions/retention.ts
//   computeRetention = salience * exp(-λ·deltaT) + σ·Σ(1/daysSinceAccess)
//   computeSalience  = typeWeight[type] + min(0.2, accessCount*0.02)
//   tier 阈值：hot≥0.7 / warm≥0.4 / cold≥0.15 / evictable<0.15
//
// 主项目落地映射（对齐 结果计划指南.md 3.1）：
//   working    → conversations/messages/process_details（当前 turn 上下文，不入衰减）
//   episodic   → project_facts（category=target/finding，目标事实）
//   semantic   → knowledge_base_items/knowledge_embeddings（通用情报）
//   procedural → skills/workflow_definitions（已固化流程，不入衰减）
//
// 本文件只含纯函数（不依赖 database/sql），可在 CGO_ENABLED=0 下单测。
// SQL 层的 access_count 持久化由 migrate_provenance.go + 后续查询承担。
// ============================================================================

// MemoryTier 记忆强度分层。
type MemoryTier string

const (
	TierHot       MemoryTier = "hot"       // 高强度，应优先注入上下文
	TierWarm      MemoryTier = "warm"      // 中强度，按需召回
	TierCold      MemoryTier = "cold"      // 低强度，仅在相关时召回
	TierEvictable MemoryTier = "evictable" // 候选淘汰/归档
)

// DecayConfig 衰减参数（对齐 retention.ts DEFAULT_DECAY）。
type DecayConfig struct {
	// Lambda 时间衰减系数（越大衰减越快）。参考默认 0.01 → 100 天衰减到 1/e。
	Lambda float64
	// Sigma 访问增强权重（越大，近期访问的记忆留存越久）。
	Sigma float64
	// TierThresholds 分层阈值（score 落区间决定 tier）。
	TierThresholds TierThresholds
}

// TierThresholds 分层阈值（对齐 retention.ts tierThresholds）。
type TierThresholds struct {
	Hot  float64 // score >= Hot → hot
	Warm float64 // Warm <= score < Hot → warm
	Cold float64 // Cold <= score < Warm → cold
	// score < Cold → evictable
}

// DefaultDecayConfig 默认衰减配置（对齐 retention.ts:12-20）。
func DefaultDecayConfig() DecayConfig {
	return DecayConfig{
		Lambda: 0.01,
		Sigma:  0.3,
		TierThresholds: TierThresholds{
			Hot:  0.7,
			Warm: 0.4,
			Cold: 0.15,
		},
	}
}

// categorySalienceWeights 项目事实 category → 基础 salience 权重。
// 对齐 retention.ts typeWeights：architecture=0.9/bug=0.7/pattern=0.8/preference=0.85/workflow=0.6/fact=0.5
// 主项目 category 语义：target/finding=高(安全事实)，note=低，procedure=中。
var categorySalienceWeights = map[string]float64{
	"target":     0.9,  // 目标资产事实，高价值
	"finding":    0.85, // 漏洞/发现，高价值
	"vuln":       0.85, // 漏洞别名
	"procedure":  0.8,  // 已固化流程
	"intel":      0.8,  // 通用情报
	"chain":      0.75, // 攻击链节点
	"credential": 0.9,  // 凭证/密钥类，最高
	"note":       0.5,  // 普通笔记
}

// baseSalienceForCategory 取 category 基础 salience，未知 category 回退 0.5。
func baseSalienceForCategory(category string) float64 {
	if w, ok := categorySalienceWeights[category]; ok {
		return w
	}
	return 0.5
}

// ComputeSalience 计算单条记忆的 salience（基础权重 + 访问加成，上限 1.0）。
// 对齐 retention.ts:46-70。
//
// 参数：
//
//	category    - project_facts.category
//	accessCount - 历史访问次数（0 表示从未被召回）
//	confidence  - project_facts.confidence（confirmed=1.0 / tentative=0.6 / deprecated=0.0）
//
// 返回 [0,1] 的 salience。
func ComputeSalience(category string, accessCount int, confidence string) float64 {
	// deprecated 记忆直接归零：已废弃的事实不应被访问强化保留，应进入淘汰候选。
	if normalizeConfidence(confidence) == "deprecated" {
		return 0.0
	}
	base := baseSalienceForCategory(category)
	// confidence 覆盖：confirmed 不变，tentative 取 max(base,0.6)。
	if normalizeConfidence(confidence) == "tentative" {
		if base < 0.6 {
			base = 0.6
		}
	}
	// 访问加成：每次 +0.02，上限 +0.2（对齐 retention.ts:68-69）
	accessBonus := math.Min(0.2, float64(accessCount)*0.02)
	return math.Min(1.0, base+accessBonus)
}

// ComputeRetention 计算留存分数（时间指数衰减 + 访问强化）。
// 对齐 retention.ts:22-44。
//
// 公式：score = salience * exp(-λ·deltaT_days) + σ·Σ(1/daysSinceAccess_i)
// 上限 1.0。
//
// 参数：
//
//	salience          - ComputeSalience 输出
//	createdAt         - 记忆创建时间
//	accessTimestamps  - 历史访问时间点（空切片表示从未被访问）
//	now               - 当前时间（注入便于测试）
//	cfg               - 衰减配置
func ComputeRetention(salience float64, createdAt time.Time, accessTimestamps []time.Time, now time.Time, cfg DecayConfig) float64 {
	if createdAt.IsZero() {
		// 无创建时间无法计算时间衰减，回退 salience
		return clamp01(salience)
	}
	deltaTDays := now.Sub(createdAt).Hours() / 24.0
	if deltaTDays < 0 {
		deltaTDays = 0 // 创建时间在未来（时钟回拨）按 0 处理
	}
	temporalDecay := math.Exp(-cfg.Lambda * deltaTDays)

	reinforcementBoost := 0.0
	for _, tAccess := range accessTimestamps {
		if tAccess.IsZero() || tAccess.After(now) {
			continue // 跳过零值和未来访问
		}
		daysSinceAccess := now.Sub(tAccess).Hours() / 24.0
		if daysSinceAccess > 0 {
			reinforcementBoost += 1.0 / daysSinceAccess
		}
	}
	reinforcementBoost *= cfg.Sigma

	return clamp01(salience*temporalDecay + reinforcementBoost)
}

// TierOf 根据 score 与阈值返回分层。
// 对齐 retention.ts:156-172。
func TierOf(score float64, cfg DecayConfig) MemoryTier {
	if score >= cfg.TierThresholds.Hot {
		return TierHot
	}
	if score >= cfg.TierThresholds.Warm {
		return TierWarm
	}
	if score >= cfg.TierThresholds.Cold {
		return TierCold
	}
	return TierEvictable
}

// RetentionScore 单条记忆的留存评分（持久化用）。
type RetentionScore struct {
	MemoryID           string     `json:"memory_id"`
	Score              float64    `json:"score"`
	Salience           float64    `json:"salience"`
	TemporalDecay      float64    `json:"temporal_decay"`
	ReinforcementBoost float64    `json:"reinforcement_boost"`
	LastAccessed       time.Time  `json:"last_accessed,omitempty"`
	AccessCount        int        `json:"access_count"`
	Tier               MemoryTier `json:"tier"`
}

// ScoreProjectFact 给一条 project_fact 计算留存评分（episodic 层）。
// accessTimestamps 为该事实的历史访问时间点（可空）。
// deprecated 事实强制归零：已废弃记忆不参与保留，访问强化不适用。
func ScoreProjectFact(f *ProjectFact, accessTimestamps []time.Time, accessCount int, now time.Time, cfg DecayConfig) *RetentionScore {
	if f == nil {
		return nil
	}
	salience := ComputeSalience(f.Category, accessCount, f.Confidence)
	if salience == 0 {
		// deprecated：直接归零，不让强化分把废弃记忆拉回热层。
		return &RetentionScore{
			MemoryID:     f.ID,
			Score:        0,
			Salience:     0,
			AccessCount:  accessCount,
			LastAccessed: latestAccess(accessTimestamps),
			Tier:         TierEvictable,
		}
	}
	score := ComputeRetention(salience, f.CreatedAt, accessTimestamps, now, cfg)
	var lastAccessed time.Time
	for _, t := range accessTimestamps {
		if t.After(lastAccessed) {
			lastAccessed = t
		}
	}
	temporalDecay := 0.0
	if !f.CreatedAt.IsZero() {
		deltaTDays := now.Sub(f.CreatedAt).Hours() / 24.0
		if deltaTDays < 0 {
			deltaTDays = 0
		}
		temporalDecay = math.Exp(-cfg.Lambda * deltaTDays)
	}
	return &RetentionScore{
		MemoryID:           f.ID,
		Score:              score,
		Salience:           salience,
		TemporalDecay:      temporalDecay,
		ReinforcementBoost: score - salience*temporalDecay,
		LastAccessed:       lastAccessed,
		AccessCount:        accessCount,
		Tier:               TierOf(score, cfg),
	}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// latestAccess 返回访问时间中的最大值（空切片返回零值）。
func latestAccess(accessTimestamps []time.Time) time.Time {
	var latest time.Time
	for _, t := range accessTimestamps {
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

func normalizeConfidence(c string) string {
	switch c {
	case "confirmed", "deprecated":
		return c
	case "tentative", "":
		return "tentative"
	default:
		return "tentative"
	}
}
