# Spec: K0a — 垂直域抽象奠基（vertical abstraction）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

把"安全垂直域"从硬编码提升为接口化抽象：定义 `Vertical` interface + `Registry` + `SecurityVertical` 首实现，在 app 启动时注册 security，为后续通用化扩展（office/ecommerce/content 等非安全域）预留接入点，**不破坏现有 108 工具 / 74 skill / 16 agent 加载**。

**奠基阶段约束**：只定义接口 + 注册 security；不实际切换 active vertical（即使 config.ActiveVertical 非 security 也只 Warn 不切换）；vertical 过滤（agent/skill 目录过滤、ToolWhitelist 收紧）留给后续批次。

## Tech Stack

- Go 1.25，纯标准库（`sync`、`strings`），零外部依赖
- `internal/vertical/` 新建独立子包，不依赖 config/security/app（可独立编译）
- 接口驱动：`Vertical` 是 interface，`SecurityVertical` 是首实现

## Commands

```bash
go vet ./internal/vertical/
go test ./internal/vertical/ -count=1
go build ./...                           # 全仓编译验证（CGO=1 mingw 路径）
CGO_ENABLED=1 CC=/c/mingw64/bin/gcc.exe go build -o cyberstrike-ai.exe cmd/server/main.go
```

## Project Structure

```
internal/vertical/vertical.go        → Vertical interface + Registry（Register/Get/SetActive/Active/List/ResolveActiveName）
internal/vertical/security.go        → SecurityVertical 首实现（Name/DefaultSystemPrompt/AgentMdDir/SkillDir/ToolWhitelist/OnboardingDoc）
internal/vertical/vertical_test.go   → Registry 并发安全 + security 默认兜底 + SetActive fail-closed 测试
internal/config/config.go            → ActiveVertical / Verticals / VerticalConfig 字段（预留，空值回退 security）
internal/app/app.go                  → 启动时 Register(security.New()) + 非 security 值 Warn
config.example.yaml                  → active_vertical: security 注释段（K0a 奠基说明）
```

## Code Style

```go
// 包注释 + 契约注释 + fail-closed 标注（匹配 internal/security 风格）
// Package vertical 提供"垂直域"抽象：把不同业务领域各自的 prompt 骨架、
// agent/skill 目录、工具白名单、onboarding 文档收敛到一个接口后面。
//
// 安全优先（fail-closed）：
//   - ToolWhitelist 返回 nil 表示"放行全部工具"（向后兼容）
//   - vertical 过滤若失败默认放行全部，绝不因抽象层故障锁死 agent
func Register(v Vertical) { ... }
```

接口小而聚焦（6 方法），返回 struct，`sync.RWMutex` 保护 Registry 并发安全。

## Testing Strategy

- `vertical_test.go`：Registry 并发 Register/Get/SetActive（`-race`）；未注册名 SetActive 静默忽略（fail-closed）；Active() 未注册时返回 nil 兜底；ResolveActiveName 空值回退 security
- 回归底线：全仓 `go build ./...` 双路径不新增 FAIL；不新增 18 agent 全可见性回归测试（奠基阶段不切换 vertical，无需）

## Boundaries

- **Always**：接口定义 + security 首实现 + app 注册；`go vet/build` 过了才算完；`config.example.yaml` 与代码同步
- **Ask first**：实际切换 active vertical（非 security 值生效）；改 ToolWhitelist 收紧现有 108 工具；改 agent/skill 目录过滤逻辑
- **Never**：删除 security 默认实现；改 Registry 为非并发安全；vertical 过滤故障时锁死 agent（必须 fail-closed 放行全部）

## Success Criteria

1. `Vertical` interface 6 方法定义齐全（Name/DefaultSystemPrompt/AgentMdDir/SkillDir/ToolWhitelist/OnboardingDoc）✅ done
2. `SecurityVertical` 首实现注册到 Registry，Active() 返回 security ✅ done
3. config.ActiveVertical 非 security 时启动只 Warn 不切换（奠基阶段约束）✅ done
4. Registry 并发安全（`-race` 测试通过）✅ done
5. 全仓 `go build ./...` EXIT=0，不破坏现有 agent 加载 ✅ done

## Open Questions

- 通用化扩展边界（K2.1 前置决策）：本轮只做安全垂直深化还是纳入"安全→通用平台"转型？影响 K0 vertical 抽象后续扩展深度。当前奠基阶段不决策。
