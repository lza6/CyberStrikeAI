# CyberStrikeAI SOP（标准操作流程）

> 本文档供开发者/部署者/维护者交接使用，确保操作不靠口口相传。

## 1. 开发 SOP

### 1.1 改代码前
1. 读 `spec.md`（仓库根）+ `tasks/todo.md` 确认当前批次范围。
2. 读 `.wolf/OPENWOLF.md` + `.wolf/cerebrum.md`（若存在）了解项目约定与 Do-Not-Repeat。
3. 读 `docs/adr/` 了解架构决策边界（如 local_mode 安全、MCP 双轨）。
4. **强制先思考后编码**：复杂改动先在 spec.md 或 tasks/ 落契约再动手。

### 1.2 改代码中
1. 匹配现有风格（包注释 + 契约注释 + fail-closed）。
2. 不硬编码 API key/上游地址（用 config.yaml + config.example.yaml）。
3. 新增纯函数配 table-driven 单测（参考 `internal/security/shellsafe_test.go`）。
4. 不动 `config.yaml`（运行态），改 `config.example.yaml` 同步示例。

### 1.3 改代码后
```bash
export PATH="/c/Users/Administrator.DESKTOP-EGNE9ND/mingw/extracted/mingw64/bin:$PATH"
export GOPROXY=https://goproxy.cn,direct && export CGO_ENABLED=1 && export CC=gcc
go vet ./...                    # 静态检查
go build -o cyberstrike-ai.exe cmd/server/main.go   # 编译
go test ./internal/security/ ./internal/cache/ ./internal/skillpackage/ -count=1  # 定向单测
```
- **基线回归对照**：`workflow_status.md` 记录了历史 FAIL 集合（security `/bin/sh` Windows、handler SQLite 句柄/重启场景），这些是环境问题非代码缺陷，**不新增 FAIL** 即合格。
- 提交规范：conventional commits（feat/fix/docs/chore），`Co-Authored-By` 由 settings.json 全局禁用故不附。

## 2. 发布 SOP

1. **版本号**：`config.yaml` + `config.example.yaml` 的 `version` 字段同步（如 `v1.8.0`）。
2. **构建**：
   ```bash
   export PATH="/c/Users/Administrator.DESKTOP-EGNE9ND/mingw/extracted/mingw64/bin:$PATH"
   export GOPROXY=https://goproxy.cn,direct && export CGO_ENABLED=1 && export CC=gcc
   go build -o cyberstrike-ai.exe cmd/server/main.go
   cd desktop && npx electron-builder --win nsis
   ```
3. **SHA256**：`sha256sum "dist/CyberStrikeAI Setup <ver>.exe"` 记录到 `workflow_status.md`。
4. **GitHub Release**：删旧 asset → 上传新 asset（`cyberstrike-ai.exe` 二进制 + 桌面安装包）。
5. **台账**：`workflow_status.md` 追加节点 done + 验证日志。
6. **推送**：`git push origin main`（origin = github.com/lza6/CyberStrikeAI）。

## 3. 回滚 SOP

- **Release 回退**：GitHub Release 页删新 asset，恢复上一版 asset。
- **代码回滚**：`git revert <commit>`（不 force push，保历史）。
- **local_mode 应急关闭**：`config.yaml` 设 `auth.local_mode: false` 重启（启用 RBAC + 登录，需 admin 密码——首启日志会打印）。
- **Redis 降级**：`config.yaml` 设 `cache.driver: memory`（或删 cache 段），重启即用进程内缓存。

## 4. 排障 SOP

### 4.1 后端起不来
1. 查 `data/logs/server.log` + `data/logs/desktop-backend.log`（桌面壳）。
2. 端口占用：`netstat -ano | findstr :8080`，`taskkill /F /PID <pid>`。
3. 单实例锁：桌面壳若双开会触发 `second-instance`，确认无残留进程。
4. SQLite 锁：删 `data/conversations.db-wal` + `data/conversations.db-shm` 重启。
5. exe 直接前台跑 segfault（bash 后台 fork + CGO 已知问题）：用 `go run cmd/server/main.go` 验证逻辑。

### 4.2 AI 通道不通
1. 桌面配置窗「测试连接」按钮（`ai:testConnection` IPC）。
2. `/api/config/list-models` 一键获取模型列表（验 base_url/api_key/provider）。
3. 检查 `config.yaml` `ai.channels.<id>` 的 api_key 是否占位符（`sk-xxxx`/`PROXY_MANAGED` 会被 `isKeyUnconfigured` 判定未配置）。
4. glm-5.2 本地代理：`http://127.0.0.1:15721/v1`，确认代理进程在跑。

### 4.3 skill 加载失败
1. `skills-lock.json` 校验：启动日志 Warn「skill 供应链锁校验发现违规」→ 运行 `go run cmd/genlock/main.go` 刷新锁。
2. verbs-gate 幽灵工具：`go run cmd/verbs-gate/main.go` 扫描，报告引用了不存在工具的 skill。
3. SKILL.md frontmatter：必须 `---` 开头 + `name`/`description` 字段（`internal/skillpackage/frontmatter.go` 校验）。

### 4.4 Redis 连不上
- 启动日志 Warn「cache.driver=redis 连接失败，降级为 memory 缓存」→ 自动降级，不阻断。
- 确认 Redis 进程：`redis-cli ping`。
- 降级后仍可用（memory 兜底），只是跨进程不共享缓存。

## 5. 授权测试 SOP

- **平台仅限授权目标**：WebShell/C2/无线工具使用前提是已获得明确授权。
- **审计留痕**：所有工具执行/hitl 决策/RBAC 拒绝都进 `audit` 表（`data/conversations.db`）。
- **破坏性操作**：HIGH_IMPACT 工具集（exec/delete-file/sqlmap/metasploit 等）需审批，未审批标记 `high_impact: true` 进审计。
- **无线工具**：`tools/wireless/`（aircrack-ng/airodump-ng 等）需 root + monitor 网卡 + 授权无线环境，部分国家/地区主动去认证可能违反电信法规，使用前确认合规。
- **local_mode**：仅限本地/桌面，生产部署前必须 `local_mode: false`。

## 6. 工具命令速查

```bash
# 构建
make build              # = go build -o cyberstrike-ai.exe cmd/server/main.go
make vet                 # = go vet ./...
make test                # = go test ./... -count=1
make clean               # 删 cyberstrike-ai.exe
make skills-lock         # 生成 skills-lock.json（go run cmd/genlock/main.go）
go run cmd/verbs-gate/main.go          # 扫 skill 幽灵工具引用（report 模式 exit 0）
go run cmd/verbs-gate/main.go -strict  # 严格模式（发现幽灵 exit 1，CI 门禁）
```
