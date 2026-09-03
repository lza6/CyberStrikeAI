package microagent

import (
	"fmt"
	"strings"
)

// Microagent 单个可插拔上下文单元。移植自 openhands/microagent/microagent.py:17-27 BaseMicroagent。
// 字段与子类行为合并到单一 struct（用 Type 区分），避免 Go 的继承复杂度。
type Microagent struct {
	// Name 唯一名（来自路径或 metadata.name）。
	Name string `json:"name"`
	// Content 正文内容（frontmatter 之后的 Markdown）。
	Content string `json:"content"`
	// Metadata frontmatter 元数据。
	Metadata Metadata `json:"metadata"`
	// Source 源文件路径（用于调试/热重载定位）。
	Source string `json:"source"`
	// Type 类型（与 Metadata.Type 冗余缓存，便于 switch）。
	Type MicroagentType `json:"type"`
}

// MatchTrigger 关键词匹配。移植自 openhands/microagent/microagent.py:189-199 match_trigger。
// 小写 substring 包含即命中，返回命中的 trigger（第一个）；无命中返回空串。
// 仅对 TypeKnowledge/TypeTask 有效；TypeRepo 返回空串（always-on 不靠触发）。
func (a *Microagent) MatchTrigger(message string) string {
	if a == nil {
		return ""
	}
	if a.Type != TypeKnowledge && a.Type != TypeTask {
		return ""
	}
	lower := strings.ToLower(message)
	for _, t := range a.Metadata.Triggers {
		if t == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(t)) {
			return t
		}
	}
	return ""
}

// IsRepo 是否 always-on（TypeRepo）。
func (a *Microagent) IsRepo() bool { return a != nil && a.Type == TypeRepo }

// IsKnowledge 是否关键词触发（TypeKnowledge/TypeTask）。
func (a *Microagent) IsKnowledge() bool {
	return a != nil && (a.Type == TypeKnowledge || a.Type == TypeTask)
}

// validate 基本校验：name 非空、type 合法、knowledge/task 必须有 triggers。
// 移植自 openhands/microagent/microagent.py:184-187 与 218-220 子类校验。
func (a *Microagent) validate() error {
	if a == nil {
		return fmt.Errorf("microagent is nil")
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("microagent name is empty (source=%s)", a.Source)
	}
	switch a.Type {
	case TypeKnowledge, TypeRepo, TypeTask:
	default:
		return fmt.Errorf("microagent %s: invalid type %q (valid: knowledge/repo/task)", a.Name, a.Type)
	}
	if (a.Type == TypeKnowledge || a.Type == TypeTask) && len(a.Metadata.Triggers) == 0 {
		return fmt.Errorf("microagent %s: type=%s but no triggers", a.Name, a.Type)
	}
	// repo 类型带 triggers 语义无害（always-on 忽略 triggers），不阻断加载。
	return nil
}

// inferType 按 OpenHands 推断规则推断类型。
// 移植自 openhands/microagent/microagent.py:140-155。
// 规则：inputs 非空 → task；triggers 非空 → knowledge；否则 repo。
func inferType(m Metadata) MicroagentType {
	if len(m.Inputs) > 0 {
		// task 类型：OpenHands 自动追加 /{name} trigger（microagent.py:143-149）。
		// 由 loader 负责，此处仅返回类型。
		return TypeTask
	}
	if len(m.Triggers) > 0 {
		return TypeKnowledge
	}
	return TypeRepo
}
