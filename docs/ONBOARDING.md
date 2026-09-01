# CyberStrikeAI 新人保姆文档（10 分钟跑通）

> 目标：让新人 10 分钟内从零到发第一条对话，理解仓库地图，避开常见坑。

## 1. 5 分钟桌面版体验

### 方式 A：下载安装包（推荐小白）
1. 下载 `CyberStrikeAI-Setup-<ver>.exe`（约 165MB，Windows x64）：
   https://github.com/lza6/CyberStrikeAI/releases/latest
2. 双击安装 → 桌面快捷方式「CyberStrikeAI」双击运行。
3. **首次启动**：弹出 AI 通道配置窗 → 选 provider 预设（DeepSeek/Qwen/GLM/Ollama 等）→ 填 base_url/api_key/model → 「测试连接」→ 「一键获取模型列表」选模型 → 保存并启动。
4. 启动画面（深色 + 进度动画）→ 后端就绪 → 主窗口（对话页）。
5. 免登录（`local_mode` 默认开），直接发对话。

### 方式 B：源码构建（开发者）
```bash
git clone https://github.com/lza6/CyberStrikeAI.git
cd CyberStrikeAI
# Windows 需 mingw64 + Go 1.25 + CGO
export PATH="/path/to/mingw64/bin:$PATH"
export CGO_ENABLED=1 && export CC=gcc && export GOPROXY=https://goproxy.cn,direct
go build -o cyberstrike-ai.exe cmd/server/main.go
./cyberstrike-ai.exe -config config.yaml --http   # 后端
# 或桌面壳：cd desktop && npm install && npx electron-builder --win nsis
```

## 2. 仓库地图（每个顶级目录一句话）

```
cyberstrike-ai.exe          # 后端二进制（go build 产物）
cmd/server/main.go          # 后端入口
cmd/genlock/                # 生成 skills-lock.json
cmd/verbs-gate/             # skill 工具引用漂移门扫描
internal/app/                # 路由 + 中间件挂载 + 启动逻辑
internal/security/           # 鉴权 + SSRF urlguard + shellsafe + secureheaders + ratelimit + executor
internal/handler/            # Gin handlers（auth/config/webshell/playbooks/systemprompts/update...）
internal/multiagent/         # Eino ADK 多代理（deep/plan_execute/supervisor）+ 中间件
internal/mcp/                # MCP server + builtin 工具
internal/skillpackage/       # skill 加载 + skills-lock + verbs-gate
internal/cache/              # Cache-Aside（memory 默认 + redis 可选）
internal/config/             # config.yaml 解析 + LoadToolsFromDir（递归子目录）
web/                         # 前端（templates + static/js + i18n）
desktop/                     # Electron 壳（main.js + tray.js + splash.html）
tools/                       # 106+ 工具 yaml（含 wireless/ 子目录 9 个无线工具）
skills/                      # 28 skill 包（每个含 SKILL.md）
agents/                      # 16 个 agent markdown
roles/                       # 14 个角色 yaml
playbooks/                   # 8 个攻击剧本 yaml
prompts/                     # 系统提示词 yaml（UI 管理激活）
docs/                        # 文档（adr/ SOP ONBOARDING zh-CN en-US）
spec.md                      # 当前批次契约（spec-driven workflow）
tasks/                       # plan.md + todo.md
workflow_status.md           # 任务台账（单一事实源）
skills-lock.json             # skill 供应链锁（SHA256）
config.yaml / config.example.yaml  # 运行态配置 / 示例配置
```

## 3. 关键能力入口

| 能力 | 入口 | 说明 |
|------|------|------|
| 对话 | 主窗对话页 → 选角色 + 编排模式（eino_single/deep/plan_execute/supervisor） | SSE 流式，reasoning + response 双流 |
| AI 通道 | 系统设置 → 基本设置 → AI 通道配置 | provider 预设 + 一键模型列表 + 当前生效徽章 |
| 系统提示词 | 系统设置 → 系统提示词管理 | prompts/ yaml CRUD + 激活（内存热生效） |
| 版本检查 | 系统设置 → 版本与更新 | 连 GitHub lza6 releases/latest |
| 攻击剧本 | 左侧导航 → 攻击剧本 | 8 个 playbook 卡片 |
| 工具 | 对话 `@提及` 或 `/api/config/tools` | 106+ 工具（含 9 无线） |
| skill | agent 自动渐进披露 | 28 skill 包 |
| WebShell | 左侧导航 → WebShell 管理 | SSRF 防护（私有 IP 拦截） |
| 桌面托盘 | 系统托盘图标 | 显示主窗/打开Web/重启后端/退出 |

## 4. 常见 5 个坑

1. **exe 直接前台跑 segfault**：bash 后台 fork + CGO 已知问题，用 `go run` 或 Electron 子进程跑。
2. **AI 通道报 401**：api_key 是占位符（`sk-xxxx`/`PROXY_MANAGED`），换成真实 key。
3. **skill 加载 Warn**：`skills-lock.json` 违规 → `make skills-lock` 刷新；幽灵工具 → `go run cmd/verbs-gate/main.go` 看。
4. **8080 端口占用**：`netstat -ano | findstr :8080` + `taskkill /F /PID`，或桌面壳单实例锁已拦双开。
5. **CSP 阻断前端**：当前 CSP 含 `'unsafe-inline'`（有意妥协），若收紧需先迁移 265 处 inline onclick（P2）。

## 5. 下一步

- 读 `docs/adr/` 了解架构决策。
- 读 `docs/SOP.md` 了解开发/发布/回滚/排障流程。
- 读 `workflow_status.md` 了解当前任务进度与验证证据。
- 改代码前先读 `spec.md` + `tasks/todo.md` 确认批次范围。
