# Spec: K4 — 统一 home 目录默认接入（unified home directory）

> 回溯 spec（spec-driven-development 规范）。本批次已落地（done），spec 用于后续改代码前判断是否过时。

## Objective

把数据目录统一收敛到 `~/.cyberstrikeai/`（`$CYBERSTRIKEAI_HOME` → `$HOME/.cyberstrikeai`），`config.Load()` 在 HomeDir 为空时自动回退 `storage.HomeDir()`，**不触发迁移**（Load 保持纯解析，迁移仍由 app.go 启动时做）。为 K0b SQLite blackboard、knowledge 共库管理提供路径底座。

**奠基阶段约束**：只做 Load 回退 + example 注释；不触发迁移（迁移仍由 app.go 启动时做）；env 覆盖优先级不变（显式 > env > 默认）。

## Tech Stack

- Go 1.25，`internal/config` + `internal/storage`（已有包）
- 纯解析，零 IO 副作用（Load 不创建目录、不迁移数据）

## Commands

```bash
go vet ./internal/config/
go test ./internal/config/ -count=1
go build ./...
```

## Project Structure

```
internal/config/config.go            → K4：Load() 尾部回退 storage.HomeDir()（$CYBERSTRIKEAI_HOME → $HOME/.cyberstrikeai）
internal/storage/                    → HomeDir() 实现（env 查找 + 默认 $HOME/.cyberstrikeai）
config.example.yaml                  → K4：storage.home_dir 注释段（说明 env 回退）
```

## Code Style

```go
// K4：cfg.Storage.HomeDir 空时回退 storage.HomeDir()
// 不触发迁移（Load 保持纯解析，迁移仍由 app.go 启动时做）
if cfg.Storage.HomeDir == "" {
    cfg.Storage.HomeDir = storage.HomeDir()
}
```

匹配 config 包纯解析风格：Load 只读 + 回退，不创建目录、不迁移数据。

## Testing Strategy

- `config_test.go`：env 回退（$CYBERSTRIKEAI_HOME → HomeDir）；显式覆盖 env；部分字段整 key 覆盖 3 场景 PASS
- 回归底线：全仓 `go build ./...` EXIT=0；不破坏现有 config 加载

## Boundaries

- **Always**：Load 尾部回退；env 覆盖优先级不变（显式 > env > 默认）；config.example.yaml 与代码同步
- **Ask first**：改 env 名（$CYBERSTRIKEAI_HOME）；改默认路径（$HOME/.cyberstrikeai）；在 Load 触发迁移
- **Never**：在 Load 创建目录（破坏纯解析）；改 env 优先级；删除 HomeDir() 回退

## Success Criteria

1. `config.Load()` 在 HomeDir 空时回退 `storage.HomeDir()` ✅ done
2. env 回退（$CYBERSTRIKEAI_HOME → $HOME/.cyberstrikeai）测试 PASS ✅ done
3. 显式覆盖 env（显式 > env > 默认）测试 PASS ✅ done
4. 部分字段整 key 覆盖测试 PASS ✅ done
5. Load 保持纯解析，不触发迁移（迁移仍由 app.go 启动时做）✅ done
6. config.example.yaml 注释更新 ✅ done

## Open Questions

- 迁移逻辑（app.go 启动时从旧路径迁移到 ~/.cyberstrikeai）—— 当前不触发，留给 app.go 启动时做
