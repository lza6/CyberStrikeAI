// Package permissions 提供 tool 级权限模式决策器。
//
// 设计移植自参考项目 OpenHarness-main 的 src/openharness/permissions/（modes.py + checker.py）。
// 适配 CyberStrikeAI (Go)：
//   - 与 internal/security/rbac.go 互补：rbac 管角色级（哪些角色能调哪些 tool），
//     本包管 mode 级（default/plan/full_auto 三态 + glob path rule + command deny pattern）。
//     调用顺序：rbac 通过 → permissions.Evaluate 决策。
//   - glob 匹配用本包 glob.go 的自定义 fnmatch 等价实现（* 跨 /，支持 []abc] 字面量 ]），
//     而非 Go 标准库 path.Match（其 * 不跨 /，且无 [!seq]）。
package permissions

// PermissionMode 标识权限模式。移植自 OpenHarness permissions/modes.py:8。
type PermissionMode string

const (
	ModeDefault  PermissionMode = "default"   // 变更工具需确认
	ModePlan     PermissionMode = "plan"      // 阻塞所有变更工具直到退出 plan 模式
	ModeFullAuto PermissionMode = "full_auto" // 自动允许所有工具
)

// PathRule 是一条 glob 路径权限规则。移植自 OpenHarness permissions/checker.py:25。
type PathRule struct {
	Pattern string // glob 模式（如 "/safe/**" 或 "*.tmp"）
	Allow   bool   // true=允许，false=拒绝
}

// PermissionDecision 是权限检查结果。移植自 OpenHarness permissions/checker.py:16。
type PermissionDecision struct {
	Allowed              bool
	RequiresConfirmation bool
	Reason               string
}

// Settings 是 PermissionChecker 的配置。移植自 OpenHarness PermissionSettings。
type Settings struct {
	Mode           PermissionMode
	AllowedTools   []string  // 显式允许列表
	DeniedTools    []string  // 显式拒绝列表（优先级最高）
	PathRules      []PathRule
	DeniedCommands []string  // 命令 deny 模式（如 "rm -rf *"）
}

// Checker 评估工具调用是否允许。移植自 OpenHarness permissions/checker.py:32。
type Checker struct {
	settings Settings
}

// New 创建权限检查器。移植自 OpenHarness permissions/checker.py:35。
func New(settings Settings) *Checker {
	// 规范化 path rules：过滤空 pattern
	var rules []PathRule
	for _, r := range settings.PathRules {
		if r.Pattern != "" {
			rules = append(rules, r)
		}
	}
	settings.PathRules = rules
	return &Checker{settings: settings}
}

// Evaluate 评估工具调用是否可立即运行。移植自 OpenHarness permissions/checker.py:50。
//
// 决策顺序（与 OpenHarness 一致）：
//  1. 显式拒绝列表 → 拒绝
//  2. 显式允许列表 → 允许
//  3. path rule 匹配（deny） → 拒绝
//  4. command deny pattern 匹配 → 拒绝
//  5. full_auto → 允许
//  6. 只读工具 → 允许
//  7. plan 模式 → 拒绝变更工具
//  8. default 模式 → 变更工具需确认
func (c *Checker) Evaluate(toolName string, isReadOnly bool, filePath, command string) PermissionDecision {
	// 1. 显式拒绝
	for _, t := range c.settings.DeniedTools {
		if t == toolName {
			return PermissionDecision{Allowed: false, Reason: toolName + " is explicitly denied"}
		}
	}
	// 2. 显式允许
	for _, t := range c.settings.AllowedTools {
		if t == toolName {
			return PermissionDecision{Allowed: true, Reason: toolName + " is explicitly allowed"}
		}
	}
	// 3. path rule
	if filePath != "" {
		for _, r := range c.settings.PathRules {
			if matchGlob(r.Pattern, filePath) {
				if !r.Allow {
					return PermissionDecision{
						Allowed: false,
						Reason:  "Path " + filePath + " matches deny rule: " + r.Pattern,
					}
				}
			}
		}
	}
	// 4. command deny pattern
	if command != "" {
		for _, pat := range c.settings.DeniedCommands {
			if matchGlob(pat, command) {
				return PermissionDecision{
					Allowed: false,
					Reason:  "Command matches deny pattern: " + pat,
				}
			}
		}
	}
	// 5. full_auto
	if c.settings.Mode == ModeFullAuto {
		return PermissionDecision{Allowed: true, Reason: "Auto mode allows all tools"}
	}
	// 6. 只读
	if isReadOnly {
		return PermissionDecision{Allowed: true, Reason: "read-only tools are allowed"}
	}
	// 7. plan 模式
	if c.settings.Mode == ModePlan {
		return PermissionDecision{
			Allowed: false,
			Reason:  "Plan mode blocks mutating tools until the user exits plan mode",
		}
	}
	// 8. default 需确认
	return PermissionDecision{
		Allowed:              false,
		RequiresConfirmation: true,
		Reason:               "Mutating tools require user confirmation in default mode",
	}
}
