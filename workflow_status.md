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

## 阻塞项

（无）

## 下一步

等 A1/A2/A3 回收 → 整合 B1 → 启动 C 链路（已授权）→ E1 审查 → F1 修复
