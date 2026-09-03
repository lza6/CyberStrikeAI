// Package vertical 提供"垂直域"抽象：把不同业务领域（安全/电商/办公/内容等）
// 各自的 prompt 骨架、agent/skill 目录、工具白名单、onboarding 文档收敛到一个
// 接口后面，让后续通用化扩展可加而不破坏现有安全 agent。
//
// K0a 奠基阶段只定义接口 + Registry + SecurityVertical 首实现，并在 app 启动时
// 注册 security。本期不实际切换 active vertical（即使 config.ActiveVertical 非
// security 也只 Warn 不切换），vertical 过滤留给后续批次。
//
// 安全优先（fail-closed）：
//   - ToolWhitelist 返回 nil 表示"放行全部工具"（向后兼容，不限制现有 108 工具）
//   - vertical 过滤若失败默认放行全部，绝不因抽象层故障锁死 agent
//   - Registry 未注册任何 vertical 时 Active() 返回 security（兜底）
package vertical

import (
	"strings"
	"sync"
)

// Vertical 描述一个垂直域的全部"加载面"——prompt、agent/skill 目录、工具白名单、
// onboarding 文档。实现方只需提供这些静态描述，Registry 负责按名查找与切换。
//
// 各方法语义：
//   - Name：唯一标识，小写短横线（如 "security" / "office" / "ecommerce"）
//   - DefaultSystemPrompt：该域单代理默认 prompt 骨架（K0a 只奠基，K2.1 拆分到独立文件）
//   - AgentMdDir：agents/*.md 子代理 Markdown 目录名（相对 configDir）
//   - SkillDir：skills/ 子目录名（相对 configDir）
//   - ToolWhitelist：nil=放行全部（fail-open 兼容）；非 nil=只允许列出的工具
//   - OnboardingDoc：新用户 onboarding 文档相对路径
type Vertical interface {
	Name() string
	DefaultSystemPrompt() string
	AgentMdDir() string
	SkillDir() string
	ToolWhitelist() []string
	OnboardingDoc() string
}

// registry 全局 vertical 注册表。进程级单例，app 启动时 Register(security.New())。
// 并发安全：Register/Get/Active 用互斥保护；启动后只读，运行期可热注册但极少。
type registry struct {
	mu         sync.RWMutex
	byName     map[string]Vertical
	activeName string
}

// 全局实例。包级变量而非导出，避免外部直接改写；通过 Register/Get/Active 访问。
var reg = &registry{byName: make(map[string]Vertical)}

// Register 注册一个 Vertical 实现。重复注册同名 vertical 用新值覆盖（启动幂等）。
// nil 实现被忽略（防误注册空实现锁死后续 Get）。
func Register(v Vertical) {
	if v == nil {
		return
	}
	name := strings.TrimSpace(strings.ToLower(v.Name()))
	if name == "" {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.byName[name] = v
	// 首次注册 security 时设为 active（向后兼容：无显式 SetActive 时默认 security）
	if reg.activeName == "" {
		reg.activeName = name
	}
}

// Get 按名查找 vertical；未注册返回 nil, false。
func Get(name string) (Vertical, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	v, ok := reg.byName[name]
	return v, ok
}

// SetActive 设置当前 active vertical 名。未注册的名静默忽略（fail-closed：
// 不切换到不存在的 vertical，保持原 active 不变）。
func SetActive(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return false
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.byName[name]; !ok {
		return false
	}
	reg.activeName = name
	return true
}

// Active 返回当前 active vertical。未注册任何 vertical 时返回 nil。
// app 启动时已 Register(security)，因此正常进程永不为 nil。
func Active() Vertical {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if reg.activeName == "" {
		return nil
	}
	return reg.byName[reg.activeName]
}

// ActiveName 返回当前 active vertical 名；未设置返回 ""。
func ActiveName() string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.activeName
}

// List 返回所有已注册 vertical 名（小写、排序后）。主要用于诊断/管理 API。
func List() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]string, 0, len(reg.byName))
	for name := range reg.byName {
		out = append(out, name)
	}
	// 排序保证稳定输出
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// DefaultActiveName 未显式配置 active vertical 时的默认名（向后兼容）。
const DefaultActiveName = "security"

// ResolveActiveName 把配置里的 active_vertical 归一化；空值回退 security。
// 用于 config 加载与 app 启动判定（不在此触发切换，切换由 app 负责）。
func ResolveActiveName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return DefaultActiveName
	}
	return s
}
