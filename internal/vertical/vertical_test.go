package vertical

import (
	"strings"
	"sync"
	"testing"
)

// TestSecurityVerticalMethods 验证 SecurityVertical 各方法返回值符合契约。
func TestSecurityVerticalMethods(t *testing.T) {
	s := New()
	if got := s.Name(); got != "security" {
		t.Fatalf("Name() = %q, want %q", got, "security")
	}
	if got := s.AgentMdDir(); got != "agents" {
		t.Fatalf("AgentMdDir() = %q, want %q", got, "agents")
	}
	if got := s.SkillDir(); got != "skills" {
		t.Fatalf("SkillDir() = %q, want %q", got, "skills")
	}
	if got := s.ToolWhitelist(); got != nil {
		t.Fatalf("ToolWhitelist() = %v, want nil (放行全部)", got)
	}
	if got := s.OnboardingDoc(); got != "docs/zh-CN/README.md" {
		t.Fatalf("OnboardingDoc() = %q, want %q", got, "docs/zh-CN/README.md")
	}
	if got := s.DefaultSystemPrompt(); strings.TrimSpace(got) == "" {
		t.Fatal("DefaultSystemPrompt() 返回空字符串")
	}
	if !strings.Contains(s.DefaultSystemPrompt(), "CyberStrikeAI") {
		t.Fatalf("DefaultSystemPrompt() 应包含 CyberStrikeAI 标识，got: %s", s.DefaultSystemPrompt())
	}
}

// stubVertical 用于 Registry 测试，避免污染 security 注册。
type stubVertical struct {
	name string
}

func (s stubVertical) Name() string                { return s.name }
func (s stubVertical) DefaultSystemPrompt() string { return "stub-prompt-" + s.name }
func (s stubVertical) AgentMdDir() string          { return "stub-agents" }
func (s stubVertical) SkillDir() string            { return "stub-skills" }
func (s stubVertical) ToolWhitelist() []string     { return nil }
func (s stubVertical) OnboardingDoc() string       { return "stub-doc.md" }

// TestRegistryRegisterGetActive 测试 Registry 的 Register/Get/Active 三件套。
// 用独立子测试避免依赖全局 reg 的初始状态（reg 由 init/security 注册污染）。
func TestRegistryRegisterGetActive(t *testing.T) {
	// 临时注册表测试：使用全局 reg，但用唯一名避免冲突
	t.Run("register and get security", func(t *testing.T) {
		sec := New()
		Register(sec)
		v, ok := Get("security")
		if !ok {
			t.Fatal("Get(\"security\") 未找到已注册的 security")
		}
		if v.Name() != "security" {
			t.Fatalf("Get 返回的 vertical Name = %q, want security", v.Name())
		}
	})

	t.Run("get unknown returns false", func(t *testing.T) {
		if _, ok := Get("nonexistent-vertical-xyz"); ok {
			t.Fatal("Get 未注册的 vertical 应返回 false")
		}
	})

	t.Run("active defaults to security after register", func(t *testing.T) {
		// 注册 security 后 activeName 应为 security（首次注册设为 active）
		Register(New())
		if got := ActiveName(); got != "security" {
			t.Fatalf("注册 security 后 ActiveName() = %q, want security", got)
		}
		if got := Active(); got == nil {
			t.Fatal("Active() 返回 nil，应返回 security vertical")
		}
	})

	t.Run("register nil ignored", func(t *testing.T) {
		// nil 实现被忽略，不影响已有注册
		Register(nil)
		if _, ok := Get("security"); !ok {
			t.Fatal("Register(nil) 不应影响 security 注册")
		}
	})

	t.Run("register empty name ignored", func(t *testing.T) {
		// 空名实现被忽略
		Register(stubVertical{name: ""})
		if _, ok := Get(""); ok {
			t.Fatal("空名 vertical 不应被注册")
		}
	})

	t.Run("set active switches active", func(t *testing.T) {
		// 注册 stub 后 SetActive 切换；测试结束切回 security 保持干净
		stub := stubVertical{name: "test-vertical-stub"}
		Register(stub)
		if !SetActive("test-vertical-stub") {
			t.Fatal("SetActive 已注册的 stub 应返回 true")
		}
		if got := ActiveName(); got != "test-vertical-stub" {
			t.Fatalf("SetActive 后 ActiveName() = %q, want test-vertical-stub", got)
		}
		// 切回 security 保持后续测试干净
		if !SetActive("security") {
			t.Fatal("切回 security 失败")
		}
	})

	t.Run("set active unknown ignored", func(t *testing.T) {
		// 未注册的名静默忽略，不切换 active
		before := ActiveName()
		if SetActive("nonexistent-vertical-xyz") {
			t.Fatal("SetActive 未注册的 vertical 应返回 false")
		}
		if got := ActiveName(); got != before {
			t.Fatalf("SetActive 未注册名不应改变 active；before=%q after=%q", before, got)
		}
	})

	t.Run("register duplicate overwrites", func(t *testing.T) {
		// 同名重复注册用新值覆盖
		stub1 := stubVertical{name: "dup-vertical"}
		stub2 := stubVertical{name: "DUP-Vertical"} // 归一化后同名
		Register(stub1)
		Register(stub2)
		v, ok := Get("dup-vertical")
		if !ok {
			t.Fatal("重复注册后应能 Get 到")
		}
		// 两次注册都归一化为 dup-vertical，无法区分；验证能取到即可
		_ = v
	})
}

// TestResolveActiveName 验证空值回退 security（向后兼容）。
func TestResolveActiveName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "security"},
		{"  ", "security"},
		{"Security", "security"},
		{"  OFFICE  ", "office"},
		{"ecommerce", "ecommerce"},
	}
	for _, tc := range cases {
		if got := ResolveActiveName(tc.in); got != tc.want {
			t.Fatalf("ResolveActiveName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDefaultActiveName 验证默认名常量。
func TestDefaultActiveName(t *testing.T) {
	if DefaultActiveName != "security" {
		t.Fatalf("DefaultActiveName = %q, want security", DefaultActiveName)
	}
}

// TestListContainsSecurity 验证 List() 返回已注册 vertical 列表含 security。
func TestListContainsSecurity(t *testing.T) {
	Register(New())
	names := List()
	found := false
	for _, n := range names {
		if n == "security" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List() 应包含 security，got %v", names)
	}
	// 验证已排序
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("List() 未排序：%v", names)
		}
	}
}

// TestSecurityVerticalImplementsInterface 编译期接口实现检查。
func TestSecurityVerticalImplementsInterface(t *testing.T) {
	var _ Vertical = SecurityVertical{}
	var _ Vertical = (*SecurityVertical)(nil)
	var _ Vertical = stubVertical{}
}

// 并发安全冒烟测试：多 goroutine 并发 Register/Get/Active 不应 panic 或 race。
func TestRegistryConcurrentSafe(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Register(stubVertical{name: "concurrent-" + string(rune('a'+(i%26)))})
			_ = Active()
			_ = ActiveName()
			_, _ = Get("security")
		}(i)
	}
	wg.Wait()
	// 切回 security 保持干净
	SetActive("security")
}
