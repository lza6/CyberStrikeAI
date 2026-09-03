# Spec: K0c — skillpackage 递归扫描（recursive skill scanning）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

把 `internal/skillpackage` 的 skill 扫描从顶层 `os.ReadDir` 升级为 `filepath.WalkDir` 递归扫描，支持子目录垂直包（`skills/security/sub/SKILL.md`），为 K0a vertical 抽象的 SkillDir 过滤做准备。**不破坏现有 74 skill 加载**。

**奠基阶段约束**：只改扫描方式（ReadDir → WalkDir），name 字段语义从"直接父目录名"改为"SKILL.md 所在目录相对 skillsDir 的路径"，向后兼容顶层 skill（name 仍为单段目录名）。

## Tech Stack

- Go 1.25，纯标准库（`path/filepath`、`io/fs`、`crypto/sha256`、`sort`）
- `internal/skillpackage/` 已有包，零新增依赖

## Commands

```bash
go vet ./internal/skillpackage/
go test ./internal/skillpackage/ -count=1
make skills-lock                        # 生成 skills-lock.json（SHA256 锁）
make verbs-gate                         # 扫 skill→tool 引用漂移（report 模式 exit 0）
make verbs-gate-strict                   # 严格模式，发现幽灵工具 exit 1
go build ./cmd/genlock                   # 锁生成器
go build ./cmd/verbs-gate                # verbs-gate CLI
```

## Project Structure

```
internal/skillpackage/lock.go        → walkSkillMDs：WalkDir 递归扫描 + SHA256 + name 语义（relPath）
internal/skillpackage/lock_test.go    → 顶层 + 子目录 + 嵌套垂直包扫描测试
internal/skillpackage/verbs_gate.go  → WalkDir + vertical 过滤（K0a 预留接入点）
internal/skillpackage/verbs_gate_test.go → 幽灵工具检测 + vertical 过滤测试
internal/skillpackage/recursive_test.go   → K0c 递归扫描回归测试
internal/skillpackage/service.go     → SkillService（扫描 + 锁 + 校验编排）
internal/skillpackage/types.go        → SkillSpec / LockEntry 等结构
internal/skillpackage/frontmatter.go  → SKILL.md frontmatter 解析
internal/skillpackage/content.go      → SKILL.md 内容读取
internal/skillpackage/io.go           → 文件 IO 封装
internal/skillpackage/layout.go        → skills/ 目录布局
internal/skillpackage/validate.go     → skill 包校验
internal/skillpackage/frontmatter_test.go → frontmatter 解析测试
skills-lock.json                     → 生成产物（74 skill SHA256 锁清单）
```

## Code Style

```go
// 包注释 + 设计来源 + WalkDir 语义说明（匹配 internal/skillpackage 风格）
// walkSkillMDs 递归遍历 skillsDir，收集每个 SKILL.md 文件的信息。
// 用 filepath.WalkDir 替代原先的 os.ReadDir 顶层扫描，
// 以支持子目录垂直包（skills/security/sub/SKILL.md）。
// name 字段使用 SKILL.md 相对 skillsDir 的目录路径（如 "pentesterflow/recon"），
// 而非仅直接父目录名（"recon"），以避免不同垂直包下同名子目录冲突。
func walkSkillMDs(skillsDir string) ([]walkedSkill, error) { ... }
```

## Testing Strategy

- `recursive_test.go`：顶层 `skills/<name>/SKILL.md` + 子目录 `skills/<grp>/<name>/SKILL.md` 均被扫到；name 语义正确（relPath）；向后兼容顶层 skill（name 仍为单段）
- `lock_test.go`：skills-lock.json 生成 + 校验（新增/删除/篡改三类违规只 Warn 不阻断）
- `verbs_gate_test.go`：skill→tool 引用漂移检测（幽灵工具 Violation 清单）
- 回归底线：`make skills-lock` 生成 74 skill 锁；`make verbs-gate` exit 0；全仓不新增 FAIL

## Boundaries

- **Always**：WalkDir 递归扫描；name 用 relPath（避免同名冲突）；向后兼容顶层 skill；锁校验失败只 Warn 不阻断启动
- **Ask first**：改 name 语义（影响 skills-lock.json 内容）；改 frontmatter schema；改 verbs-gate 严格模式为 exit 1
- **Never**：改回 ReadDir 顶层扫描（破坏子目录垂直包）；锁校验失败阻断启动（生产兼容优先）；删除 skills-lock.json

## Success Criteria

1. WalkDir 递归扫描，顶层 + 子目录 SKILL.md 均被扫到 ✅ done
2. name 字段用 relPath，向后兼容顶层 skill（单段目录名）✅ done
3. `make skills-lock` 生成 74 skill 锁清单（SHA256）✅ done
4. 锁校验失败只 Warn 不阻断启动 ✅ done
5. verbs-gate 扫 skill→tool 引用漂移，幽灵工具报 Violation 清单 ✅ done
6. 全仓 `go build ./...` EXIT=0 ✅ done

## Open Questions

- vertical 过滤接入（K0a SkillDir）：K0c 已预留 WalkDir + vertical 过滤接入点，实际切换 vertical 后过滤 agent/skill 目录留给 K0a 后续批次
