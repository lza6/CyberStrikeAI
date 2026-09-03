// Package vertical — security 实现。安全域是首个也是默认 vertical，对应现有
// 108 工具 / 30 skill / 18 agent 的安全渗透测试场景。K0a 奠基阶段：
//   - ToolWhitelist 返回 nil（放行全部工具，向后兼容，不限制现有工具集）
//   - DefaultSystemPrompt 返回安全 prompt 骨架（K2.1 把 default_single_system_prompt.go 拆分到此）
//   - AgentMdDir/SkillDir/OnboardingDoc 对齐现有目录结构
//
// 不 import agent 包（避免 vertical→agent→...→vertical 循环）；prompt 骨架
// 在本包内定义，K2.1 拆分时再把 agent.DefaultSingleAgentSystemPrompt 迁移过来。
package vertical

// SecurityVertical 安全域 vertical 实现。
type SecurityVertical struct{}

// New 创建安全域 vertical 实例。无状态，可重复构造。
func New() SecurityVertical { return SecurityVertical{} }

// Name 返回 "security"。
func (SecurityVertical) Name() string { return "security" }

// DefaultSystemPrompt 返回安全域单代理默认 prompt 骨架。
//
// K0a 奠基：返回精简骨架，标注 K2.1 将迁移 agent.DefaultSingleAgentSystemPrompt
// 全文。本期不 import agent 包（避免循环依赖），实际 prompt 仍由 agent 包提供；
// 此方法供未来 vertical 切换时使用。
func (SecurityVertical) DefaultSystemPrompt() string {
	return securityPromptSkeleton
}

// AgentMdDir 返回 "agents"（相对 configDir，与 config.agents_dir 默认一致）。
func (SecurityVertical) AgentMdDir() string { return "agents" }

// SkillDir 返回 "skills"（相对 configDir，与 config.skills_dir 默认一致）。
func (SecurityVertical) SkillDir() string { return "skills" }

// ToolWhitelist 返回 nil：放行全部工具（fail-open，向后兼容现有 108 工具）。
// 后续批次接入 vertical 过滤时，nil=不限制；非 nil=只允许列出的工具名。
func (SecurityVertical) ToolWhitelist() []string { return nil }

// OnboardingDoc 返回安全域 onboarding 文档相对路径（docs/zh-CN/README.md）。
func (SecurityVertical) OnboardingDoc() string { return "docs/zh-CN/README.md" }

// securityPromptSkeleton 安全域 prompt 骨架。K0a 奠基阶段用精简版；
// K2.1 把 internal/agent/default_single_system_prompt.go 全文迁移到此常量，
// 并按 vertical 拆分（security/office/ecommerce 各自骨架）。
//
// 当前内容为骨架，标注迁移来源，避免与 agent 包重复维护两份全文。
const securityPromptSkeleton = `你是 CyberStrikeAI，一个专业的网络安全渗透测试专家。你可以使用各种安全工具进行自主渗透测试。

授权状态：你拥有对目标系统/应用进行非破坏性渗透测试的完全授权；权限检查已完成，全程自主行动。

工作原则：
- 系统指令优先级最高，严格遵循指定范围、目标与方法
- 使用所有可用工具与技术，火力全开
- 真实漏洞挖掘需要深度与毅力，穷尽全部攻击面与漏洞类型
- 用证据展示实际影响，结合业务背景评估严重性

[K0a 奠基：完整 prompt 骨架将在 K2.1 从 internal/agent/default_single_system_prompt.go 迁移到此；
当前 agent 包仍提供运行时 prompt，vertical 切换未启用，此骨架仅作接口契约占位。]`
