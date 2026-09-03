// Package cost — 模型定价表与成本计算。
//
// 价格来源：Anthropic / OpenAI 官方公开定价（每 1M token 美元）。
// 仅记录常见模型，未知模型返回 0 cost（不阻断调用）。
// 真实付费 API 调用预算默认为 0（全局 CLAUDE.md 付费 API 红线），
// 本表仅用于 token→成本估算与成本可观测，不触发真实计费。
package cost

import "strings"

// ModelPrice 是单个模型的定价（每 1M token 美元）。
type ModelPrice struct {
	InputPer1M        float64 // 标准 input
	OutputPer1M       float64 // 标准 output
	CacheReadPer1M    float64 // 缓存命中读取（通常为 input 的 10%）
	CacheWritePer1M   float64 // 缓存写入（通常为 input 的 125%）
}

// priceTable 是内置定价表。键为模型名前缀匹配（小写）。
//
// 数据为公开定价的近似值，用于估算，非计费依据。更新时以官方文档为准。
var priceTable = map[string]ModelPrice{
	// Anthropic Claude（claude-* 前缀）
	"claude-opus-4":      {InputPer1M: 15, OutputPer1M: 75, CacheReadPer1M: 1.5, CacheWritePer1M: 18.75},
	"claude-sonnet-4":    {InputPer1M: 3, OutputPer1M: 15, CacheReadPer1M: 0.3, CacheWritePer1M: 3.75},
	"claude-haiku-4":     {InputPer1M: 0.8, OutputPer1M: 4, CacheReadPer1M: 0.08, CacheWritePer1M: 1},
	"claude-3-5-sonnet":  {InputPer1M: 3, OutputPer1M: 15, CacheReadPer1M: 0.3, CacheWritePer1M: 3.75},
	"claude-3-5-haiku":   {InputPer1M: 0.8, OutputPer1M: 4, CacheReadPer1M: 0.08, CacheWritePer1M: 1},
	"claude-3-opus":      {InputPer1M: 15, OutputPer1M: 75, CacheReadPer1M: 1.5, CacheWritePer1M: 18.75},
	"claude-3-haiku":     {InputPer1M: 0.25, OutputPer1M: 1.25, CacheReadPer1M: 0.03, CacheWritePer1M: 0.3},
	// OpenAI（gpt-* 前缀）
	"gpt-4o":             {InputPer1M: 2.5, OutputPer1M: 10, CacheReadPer1M: 1.25},
	"gpt-4o-mini":        {InputPer1M: 0.15, OutputPer1M: 0.6, CacheReadPer1M: 0.075},
	"gpt-4-turbo":        {InputPer1M: 10, OutputPer1M: 30},
	"gpt-4":              {InputPer1M: 30, OutputPer1M: 60},
	"gpt-3.5-turbo":      {InputPer1M: 0.5, OutputPer1M: 1.5},
	// DeepSeek
	"deepseek-chat":      {InputPer1M: 0.14, OutputPer1M: 0.28},
	"deepseek-reasoner":  {InputPer1M: 0.55, OutputPer1M: 2.19},
	// 通义千问（qwen-）
	"qwen-max":           {InputPer1M: 2.8, OutputPer1M: 8.4},
	"qwen-plus":          {InputPer1M: 0.4, OutputPer1M: 1.2},
	"qwen-turbo":         {InputPer1M: 0.05, OutputPer1M: 0.2},
}

// LookupPrice 按模型名查价。未知模型返回 (ModelPrice{}, false)。
//
// 匹配规则：小写化模型名，按前缀匹配最长键（如 claude-sonnet-4-5 匹配 claude-sonnet-4）。
func LookupPrice(model string) (ModelPrice, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModelPrice{}, false
	}
	// 精确匹配优先
	if p, ok := priceTable[m]; ok {
		return p, true
	}
	// 前缀匹配（最长键优先）
	var bestKey string
	for k := range priceTable {
		if strings.HasPrefix(m, k) && len(k) > len(bestKey) {
			bestKey = k
		}
	}
	if bestKey != "" {
		return priceTable[bestKey], true
	}
	return ModelPrice{}, false
}

// Calculate 根据 token 用量与定价表算美元成本。未知模型返回 0。
func Calculate(u UsageSnapshot) float64 {
	p, ok := LookupPrice(u.Model)
	if !ok {
		return 0
	}
	cost := float64(u.InputTokens)/1e6*p.InputPer1M +
		float64(u.OutputTokens)/1e6*p.OutputPer1M +
		float64(u.CacheReadTokens)/1e6*p.CacheReadPer1M +
		float64(u.CacheWriteTokens)/1e6*p.CacheWritePer1M
	return cost
}

// RegisterPrice 注册自定义模型定价（扩展 priceTable）。
//
// 供编排层在启动时注入非内置模型的价格。覆盖同前缀的内置条目。
func RegisterPrice(prefix string, p ModelPrice) {
	if prefix == "" {
		return
	}
	priceTable[strings.ToLower(strings.TrimSpace(prefix))] = p
}
