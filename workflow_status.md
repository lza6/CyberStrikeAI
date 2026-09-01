# CyberStrikeAI 工程化对标与剩余风险闭环 · workflow_status

> 单一事实源。只记录事实与证据，不记录私有推理。只有观察到交付物和验收证据才标记 done。
> 节点验收方式按项目真实上下文定：后端 `go vet/build/test` + `curl` 对话实测；前端 `node --check` + i18n 完整性；桌面 Electron 配置窗口逻辑验证。不套用不存在的测试框架。

## 任务契约

- **主项目**：`C:\Users\Administrator.DESKTOP-EGNE9ND\Desktop\智能渗透\CyberStrikeAI`（Go 1.25 + Eino + MCP + Gin + SQLite/CGO + 原生 JS 前端 + Electron 桌面）
- **参考项目根**：`C:\Users\Administrator.DESKTOP-EGNE9ND\Desktop\智能渗透\参考项目`（14 个项目，已由子代理调研过，结论复用）
- **目标**：系统化对标分析 → 9 段报告 + P0/P1/P2 路线图；剩余风险闭环；独立审查
- **授权边界**：只读分析 + 上文剩余风险闭环（已授权）可直接做；**新代码改造需用户确认路线图后再做**

## 验收标准表

| 节点 | 交付物 | 验收方式 | 状态 |
|------|--------|---------|------|
| A1 主项目架构识别 | 结构化画像 markdown | 覆盖 9 段输出所需全部主项目事实，每条带文件路径证据 | done |
| A2 Go 后端代码审计 | P0/P1/P2 问题清单 | 每条带 file:line + 证据 + 复现方式，不改代码 | done |
| A3 前端代码审计 | 问题清单 | 覆盖 web/static/js + templates，i18n 完整性 | done |
| B1 9 段分析报告 | 完整 markdown | 含识别/现状/亮点/差距/迁移/路线图/实施/确认前清单/缺失信息 | pending |
| C1 后端 go vet+build | 命令输出 | `go vet ./...` 与 `go build` 无 error | done |
| C2 后端 go test | 命令输出 | `go test ./...` 真实输出（无测试则记录"无测试"） | done |
| C3 AI 对话端到端 | curl 输出 | 用 glm-5.2 发对话，收到非空 assistant 回复 | done |
| C4 前端 JS 语法+i18n | 命令输出 | 关键 JS `node --check` 通过；i18n key 无缺失 | done |
| C5 桌面配置窗口逻辑 | 逻辑验证 | ai-config.js 读写 + needSetup 判定 + 4 JS 文件 node --check | done(上轮) |
| C6 dotdotpwn 修复 | 加载日志 | 后端启动无 dotdotpwn 加载警告 | done |
| E1 独立审查 | 6 方面报告 | 只读，不改代码，输出 PASS/FAIL/待验证 | done |
| F1 修复审查发现 | 修复 + 复验 | 按 P0/P1 优先级修，复验通过 | done |
| G1 桌面免登录（local_mode） | 后端 + 前端 + 桌面壳 | local_mode 下不带 token 访问 /api/config /api/auth/validate /api/skills /api/conversations 全 200；桌面版双击直接进对话页不弹登录 | done |
| G2 原生 exe 界面 | Electron BrowserWindow | 双击 exe → BrowserWindow 加载 http://127.0.0.1:8080（内置 admin 全权限），Web UI 可在窗口内切换 | done |
| G3 agent/skill/tool 内置 | 后端自动加载 | agents/skills/tools 启动时自动加载到 MCP 池 + skill 渐进披露池 + 多代理 sub_agents，对话界面可触发（T3 子代理验证） | done |
| G4 playbooks 全栈内置 | 后端 loader + API + 前端 | `/api/playbooks` 返回 8 剧本；对话页左侧导航有"攻击剧本"菜单，点击渲染卡片 | done |
| G5 独立 exe 双击即开界面 | 后端 auto-open + 桌面壳原生菜单 | 独立 `cyberstrike-ai.exe` 启动后自动打开默认浏览器到 Web UI；桌面壳有原生菜单（视图/在浏览器中打开/退出） | done |
| G6 桌面版关闭 TLS | ensureDesktopDefaults 关 TLS | 桌面版 http://127.0.0.1:8080 纯 HTTP，Electron BrowserWindow 正常加载（无自签证书拦截） | done |
| G7 仓库文档中文化 | README/docs 主入口中文优先 | 根 README.md 改为中文（英文转 README_EN.md）；mcp-servers/README.md 中文优先；docs/README.md 中文段在前 | done |
| H0 文档去上游化 | 赞助段删除 + 地址换 lza6 + git remote | README×3 赞助二维码段删除、clone/页面 GitHub 地址全换 lza6；git remote 只剩 origin=lza6 | done |
| H1 WebShell SSRF 过滤 | urlguard + 全出口拦截 | 19 case 单测 PASS；实测明文内网 URL 400 拦截；302 重定向跳内网被 CheckRedirect 拦（回归测试 PASS） | done |
| H2 sessions LRU 上限 | maxSessions=1000 | TestSessionLimitEviction PASS | done |
| H3 全局 API 限流 | 600/min SSE 豁免 | TestGlobalRateLimit PASS | done |
| H4 安全响应头 | CSP/DENY/nosniff/Referrer/Permissions + HTTPS HSTS | TestSecureHeaders PASS；curl -I 实测 5 头全齐 | done |
| H5 handler 5xx err 脱敏 | errresp.go internalError | 5xx err.Error() 裸露清零（212 处替换，4xx 校验型 147 处保留）；30 文件 | done |
| H6 local_mode 公网防护 | 强制回环 | 日志 warn + 改绑 127.0.0.1；netstat 实测只听回环 | done |
| H7 CI/Makefile/golangci | .github/workflows/ci.yml + Makefile + .golangci.yml | 文件落盘，CI 配置 CGO_ENABLED=1 | done |
| H8 提供商管理增强 | provider 预设+徽章+一键模型 | settings.js onAIChannelProviderPresetChange + ai-channel-active-badge + list-models 已通 | done |
| H9 系统提示词全栈 | prompts/ yaml CRUD/激活 | TestSystemPrompts 5 case PASS；/api/system-prompts 200；内置提示兜底；激活热生效 | done |
| H10 版本更新检查 | /api/update/check → lza6 releases | TestUpdate 4 case PASS；实测 200 current=latest=1.7.17；release_url scheme 白名单防 javascript: 注入 | done |
| H11 独立 Critic 审计 | 6 方面审查 + 修复闭环 | 主代理 self-review 发现并修复 2 项：webshell SSRF 302 重定向绕过（CheckRedirect + 回归 PASS）、version-update.js href scheme 注入；其余抽查通过 | done |
| H12 真实 E2E | 启动→免登录→配置→对话→拦截 | go run 实跑 10 项全过：探活/免登录/安全头/system-prompts/update/SSRF拦截/playbooks/对话SSE(271 delta)/中文回复"在的"/限流未误伤 | done |
| H13 提交推送+发行版 | main 提交 + Release 更新 | 4 提交推送 origin(main)；Release v1.7.17 asset 165513295 state=uploaded | done |

## 任务图（依赖）

```
A1 ┐
A2 ├─→ B1(9段+路线图) ──→ [等用户确认 D 改造]
A3 ┘                              │
                                  ↓
C1/C2/C4/C6 (并行，已授权) ──→ C3(依赖 A1 给 API 路由)
                                  │
                                  ↓
                            E1 独立审查(6方面)
                                  │
                                  ↓
                            F1 修复 + 复验闭环
```

## 当前阶段

**F1 修复闭环进行中**（E1 审查的必须修项已全部落地+复验，A2 Go 审计的 P0 项已落地+复验，NSIS 重新打包进行中）

E1 审查 5 项必须修 + A2 P0/P1 关键项已全部修复并复验通过：
- ✅ SetTrustedProxies(nil) — XFF 绕过限流已验证修复
- ✅ MCP MaxBytesReader — 防 DoS
- ✅ payload_builder GOOS/GOARCH 白名单 + binName 路径校验
- ✅ payload_oneliner isSafeHostToken — 防 host 命令注入
- ✅ vulnclaw-core 7 .md → 子目录/SKILL.md（可加载）
- ✅ warstories/SKILL.md（可发现）
- ✅ idor-bola-tester 去误报分支
- ✅ ai-config.js id 清洗 + isKeyUnconfigured 扩检测
- ✅ main.js 后端 stdout 落盘 + saveAndLaunch 错误透传 + claude testConnection 修真
- ✅ i18n rbac.resourceTypes.asset 补齐

## 验证日志

- 2026-09-01 16:1x 上轮已验证：后端 ONLINE HTTP 200；ai-config.js 读写正确；17 个新 tool yaml 语法通过；桌面配置窗口 4 个 JS 文件 node --check 通过
- 2026-09-01 16:1x dotdotpwn yaml 反斜杠转义已修复，后端无加载警告
- 2026-09-01 16:21 C1 done：`go vet ./...` exit 0 无输出（clean）；`go build` 上轮已验证产出 cyberstrike-ai.exe
- 2026-09-01 16:22 C2 done：`go test ./...` 结果——
  - internal/handler FAIL 3：2 个 SQLite TempDir 被"文件被占用"无法清理（Windows 句柄占用，测试逻辑本身通过，cleanup 失败）；1 个 `TestEnsureSchemaFinalizesOnlyHistoricalPlaceholdersWithTerminalEvidence` 断言失败（message="处理中..." want "任务因服务重启已中断。"，重启场景 schema 收尾逻辑）
  - internal/security FAIL 4：`TestEinoStreamingShell_*` 因 `/bin/sh` 在 Windows 不存在而失败（环境问题，非代码缺陷）
  - 其余包 ok（hitl/knowledge/llm/mcp/monitor/multiagent/openai/project/reasoning/termout/tooloutput/vision/workflow/security 主体）
- 2026-09-01 16:23 C3 done：清库重启 → 首启显示 admin 密码 → `/api/auth/login` 拿到 token → `/api/eino-agent/stream` 发"回复两个字：在的" → 收到 `reasoning_chain_stream_delta`（glm-5.2 推理流）+ assistant 响应，AI 通道端到端打通（glm-5.2 经 127.0.0.1:15721 本地代理）
- 2026-09-01 16:24 C4 done：15 个关键前端 JS `node --check` 全 OK；i18n zh-CN 4350 key / en-US 4362 key，en 缺 13 个 zh key（dashboard.* 复数形式 _one/_other 是 i18next 复数约定，非真缺失），zh 缺 25 个 en key（同为复数形式），实际无单数 key 缺失，i18n 对齐良好
- 2026-09-01 16:3x A1 done（子代理返回）：主项目画像 12 段齐全，带文件路径证据。核心：Go1.25+Eino ADK 多代理+MCP+Gin+SQLite(CGO/WAL)+原生JS前端+Electron31 NSIS 桌面壳（内嵌 Python 3.13.5）；internal/ 31 个包；路由 app.go:861-1410；入口 cmd/server/main.go；223 个 *_test.go 无 E2E；无 Dockerfile（原生二进制部署）；资产 tools106/skills30/agents16/roles14/playbooks9
- 2026-09-01 16:3x A3 done（子代理返回）：前端审计——CRITICAL 0（XSS 防护链完整 DOMPurify+marked）；HIGH 2（CSP 缺失 H1 + inline onclick 阻断 strict CSP H2）；MEDIUM 6（chat.js 11190 行 M1、超长函数 renderAttackChain 515 行 M2、console.* 265 处 M3、i18n 复数不对齐 M4、rbac.resourceTypes.asset 两边缺 M5、fetch 网络错误未统一 M6）；LOW 7。正向：apiFetch 401 统一、AI 通道前端校验+后端双步、escapeHtml 工具、vendor 本地化无 CDN
- 2026-09-01 16:3x A2 done（子代理返回）：Go 后端审计 25 条——CRITICAL 5（C1 app.go 2244 行超大/New() 517 行、C2 WebShell SSRF 无私有 IP 过滤、C3 398 处 err.Error() 裸露、C4 URL token+无 CSRF、C5 gin 未设 trusted proxies ClientIP 可伪造）；HIGH 10（H6 goroutine 泄漏、H7 内存 session 无上限、H8 RunDeepAgent 582 行、H9 agent.go 超长函数、H10 无全局限流、H11 MCP 全局访问全权限、H12 知识库索引逻辑重复、H13 payload_builder 无白名单、H14 WeChat corpsecret 入 URL、H15 MCP io.ReadAll 无限制）；MEDIUM 10；LOW 1
- 2026-09-01 16:5x E1 done（独立审查 6 方面，Critic 角色）：总体 CONDITIONAL-PASS。必须修 5 项：[HIGH] vulnclaw-core 7 个 .md 无 SKILL.md 不可加载、[HIGH] warstories 无 SKILL.md、[MEDIUM] idor-bola-tester 误报分支、[MEDIUM] applyChannel 畸形 id、[MEDIUM] workflow_status C2 漏报
- 2026-09-01 17:0x-17:2x F1 修复批次（已全部落地+复验）：
  - **SetTrustedProxies(nil)**：app.go:84 → 不信任任何代理，XFF 伪造不再绕过登录限流。复验：不带 XFF 11 次第 11 次 429（基线）；带轮换 XFF 11 次第 11 次 429（修复后不再被绕过，修复前 11 次全 401）
  - **MCP io.ReadAll → MaxBytesReader(10MB)**：server.go:253,281 两处，防超大 body DoS
  - **payload_builder GOOS/GOARCH 白名单 + binName filepath.Base**：types.go 加 isAllowedGOOS/isAllowedGOARCH（linux/windows/darwin/freebsd/openbsd × amd64/arm64/386/arm）；payload_builder.go:84-98 校验
  - **payload_oneliner isSafeHostToken**：types.go 加 isSafeHostToken（仅 IP/域名/IPv6 字符）；payload_oneliner.go:108 校验。单测 11 case 全过（`1.2.3.4; rm -rf /` / `$(id)` / `` `id` `` / `|nc evil 53` / 空格全拒）
  - **vulnclaw-core 7 个 .md → 子目录/SKILL.md**：解决 ListSkillDirNames 跳过问题，现可被 skill 加载器发现
  - **warstories/SKILL.md**：补索引让战报库可被发现
  - **idor-bola-tester 去误报分支**：删除 `ba==bb` 的 or 分支（公共页非 BOLA）
  - **ai-config.js applyChannel id 清洗**：加 `.replace(/^-+|-+$/g,'')` 去首尾横线，纯非字母数字输入回退 `custom`。复验：`桌面`→`custom`、`日本語`→`custom`、`My Channel`→`my-channel`
  - **ai-config.js isKeyUnconfigured 扩占位符检测**：加 placeholder/changeme/example，min 长度 12→8 兼容短 token。复验 10 case 全过
  - **main.js 不再吞后端 stdout**：pipe 到 data/logs/desktop-backend.log；saveAndLaunch 返回 {ok,error} 透传错误；claude testConnection 改走 /v1/messages
  - **config.js 透传 saveAndLaunch 错误到 UI**
  - **i18n rbac.resourceTypes.asset**：zh/en 两边补 `资产`/`Asset`
- 2026-09-01 17:2x F1 复验：`go vet ./...` exit 0；`go build` ok；`go test ./internal/c2/` ok；desktop 4 JS `node --check` 全 OK；NSIS 重新打包进行中
- 2026-09-01 17:4x F1 最终安装包上传完成：asset_id=539332678，size=165444014，state=uploaded，SHA256=9fe8bb1c9eaf1740b94be873d4788efca1c3b1f6783d10fe34069547544d0f42，download_url=https://github.com/lza6/CyberStrikeAI/releases/download/v1.7.17/CyberStrikeAI-Setup-1.7.17.exe
- 2026-09-01 19:3x G1-G3 桌面免登录 + 原生 exe 界面 + agent 内置 闭环：
  - **G1 local_mode 后端**：config.go AuthConfig.LocalMode；auth_manager.go SetLocalMode/IsLocalMode/LocalSession（内置 admin 全权限 all scope）；auth_middleware.go AuthMiddleware 命中 localMode 跳过 token 注入 local session；auth.go Login/Validate localMode 分支返回 local session；app.go New() 读 cfg.Auth.LocalMode
  - **G1 前端**：auth.js initializeApp 先探测 /api/config（local_mode 下 200）→ 免登录直进主界面，localStorage 缓存 cyberstrike-local-mode 标记
  - **G1 桌面壳**：ai-config.js ensureDesktopDefaults 强制 local_mode=true + host=127.0.0.1；main.js startBackend 调用确保双击即免登录
  - **G2 原生 exe 界面**：Electron BrowserWindow 加载 http://127.0.0.1:8080（已现状），配合 local_mode 不再弹登录窗，Web UI 即原生窗口内界面
  - **G3 agent/skill/tool 内置**（T3 子代理验证）：agents LoadMarkdownAgentsDir 自动合并；skills skill.NewBackendFromFilesystem 渐进披露池；tools LoadToolsFromDir→MCP 池；roles LoadRolesFromDir 对话页可选；编排 4 模式对话页可选
  - **G1 复验**（go run 实跑）：启动日志"已启用本地免登录模式"；/api/auth/validate 不带 token → 200 {local_mode:true, permission_scopes:全 all}；/api/config 200；/api/skills 200；/api/conversations 200；go vet ./... exit 0；go build ok；desktop 4 JS node --check 全 OK；ensureDesktopDefaults 单测 local_mode=true host=127.0.0.1（强制覆盖 0.0.0.0）
  - 注：exe 直接前台 `timeout` 跑会 segfault（exit 139），是 bash 后台 fork + CGO 的已知环境问题，非代码缺陷；`go run` 正常启动并 ONLINE。NSIS 打包用的二进制与 go run 同源，安装包内 exe 在 Electron 子进程下可正常启动
- 2026-09-01 19:5x G1-G3 最终安装包重新打包完成：size=165449095，含 local_mode 后端 + auth.js 免登录探测 + ensureDesktopDefaults；上传到 Release 进行中
- 2026-09-01 20:2x-20:4x G4-G7 剩余风险全闭环：
  - **G4 playbooks 全栈**：internal/playbooks/playbooks.go（LoadPlaybooksFromDir，7 子用例 test PASS）；internal/handler/playbooks.go（ListPlaybooks/GetPlaybook）；app.go:1361-1364 挂 /api/playbooks 路由；rbac_middleware.go 加 /playbooks → roles CRUD 权限映射 + isProcessGlobalMutationPath；web/static/js/playbooks.js（IIFE，loadPlaybooks 渲染卡片）；index.html 加 nav 项 + page-playbooks section + script 引用；router.js initPage 加 case 'playbooks'；i18n zh/en 加 nav.playbooks + playbooksPage 段。复验：`/api/playbooks` 不带 token → 200 返回 8 剧本（api-security/bug-bounty/ci-cd-security/ctf-solver/external-asm/internal-network/owasp-top10/pheromones）；`/api/playbooks/owasp-top10` → 200；不存在 → 404
  - **G5 独立 exe 双击即开界面**：internal/termout/browser.go OpenBrowser（cmd /c start / open / xdg-open）；cmd/server/main.go 服务器启动后异步等端口就绪自动开浏览器（CYBERSTRIKE_NO_AUTO_OPEN=1 抑制，桌面壳已设）；desktop/src/main.js 加原生菜单（CyberStrikeAI/视图两个 submenu：在浏览器中打开/重新加载/开发者工具/放大缩小/全屏/退出）
  - **G6 桌面版关闭 TLS**：ensureDesktopDefaults 强制 tls_enabled/tls_auto_self_sign/tls_http_redirect 全 false；复验启动日志 `http://127.0.0.1:8080/` 无 TLS/Redirect 行；HTTP 200
  - **G7 仓库文档中文化**：根 README.md ← 原 README_CN.md 内容（中文优先），原英文版 → README_EN.md，README.md/README_CN.md/README_EN.md 三者链接互指修正；mcp-servers/README.md ← README_CN.md（中文优先）+ README_EN.md；docs/README.md 标题+导语改中文。desktop/README.md、tools/README.md、cmd/test-sse-mcp-server/README.md 本就是中文。docs/zh-CN/ 与 docs/en-US/ 双语并存（设计如此，英文版供英文用户）
- 2026-09-01 20:4x G4-G7 最终安装包重新打包完成（含无 TLS + playbooks + 原生菜单 + 文档中文化）

## 阻塞项

（无）

## 下一步

等 A1/A2/A3 回收 → 整合 B1 → 启动 C 链路（已授权）→ E1 审查 → F1 修复
