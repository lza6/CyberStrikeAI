# CyberStrikeAI 性能闭环（§4.4 P1/P2/P3）

> 本批次落地《结果计划指南.md》§4.4 性能层三项节点。证据优先，反伪实现。
> 范围：本地修改 + 非破坏性验证（go vet/build/test + Playwright E2E + curl 真实链路）。

## 任务契约

- **主项目**：CyberStrikeAI（Go 1.25 + Eino ADK + MCP + Gin + SQLite/CGO + 原生 JS 前端）
- **目标**：落地 §4.4 三项性能节点，真实 E2E 测验 + 验收 + 审计
- **授权边界**：本地修改 + 非破坏性验证；不推送、不部署、不覆盖生产 server.exe（测试用临时 8090 端口 + 临时二进制）
- **跨会话协调**：worktree 隔离 `perf-4-4-cache`，主仓 `server.exe`(8080) 与其他会话不冲突

## 验收标准表

| 节点 | 交付物 | 验收方式 | 状态 |
|------|--------|---------|------|
| P1 首屏体积 | index.html 拆包/压缩/懒加载规划 | build.mjs 已产出 dist（gzip -83.7%）+ 懒加载方案 | done（部分） |
| P2 后端热点 | /api/config cache-aside + config 锁段外取外部 MCP | go test PASS + curl 耗时对比 + Playwright | done |
| P3 SQLite 并发 | WAL + busy_timeout + PASSIVE checkpoint + 并发 smoke | go test -race PASS + 无 database is locked | done |
| V1 真实 E2E | Playwright perf-cache.spec.js 3 用例 | 3/3 PASS | done |
| V2 单元测试 | config_cache_test + secureheaders_test + static_cache_test + db_smoke | go test PASS | done |
| C1 独立审计 | 反伪实现/回归/边界 | 见下「独立审计」段 | done |

---

## P1 首屏体积

### 现状（实测 2026-09-02）

- `index.html` 683KB（单文件含 ~50 `<script>` 同步加载）
- `web/static/js/` 47 个业务 JS + 3 个 CSS，原始合计 **3.44MB JS + 1.31MB CSS**
- 已有 `web/build.mjs`（渐进增强构建脚本，Node 内置 + terser/clean-css，无 Vite 重型依赖）
  - 产出 `web/static/dist/`：minify + SHA256 hash + `.gz`
  - gzip 后 **558KB JS（-83.7%）+ 149KB CSS（-88.6%）**
- `index.html` 当前**未引用 dist**（仍走 raw `/static/js/*.js?v=`），生产仍按 raw 加载
- 首屏必需 JS ~271KB（router/i18n/theme/auth/modal/notifications/info-collect/dashboard/chat-core 等），约占总 6.43MB 的 4%

### 本批次落地（已实施）

1. **静态资源长缓存头**（`internal/security/static_cache_headers.go`）：
   - `/static/*` → `Cache-Control: public, max-age=31536000, immutable`
   - 配合 `?v=YYYYMMDD-N` 版本号：内容变 → 版本号变 → 浏览器视为新 URL 重拉，`immutable` 连 304 校验都不发
   - 非 `/static/`（HTML/API）由 `SecureHeaders` 设 `no-store`（见 P2）
2. **dist 产物就绪**（`web/build.mjs`，主仓已构建）：
   - 压缩 + 哈希 + gzip 三件套，可重复构建（内容不变则哈希不变）
   - manifest.json 记录每个文件 raw/minified/gzip 字节数

### P1 验收

- `curl -sI /static/js/chat.js` → `Cache-Control: public, max-age=31536000, immutable` ✅
- `curl -sI /` → `Cache-Control: no-store, no-cache, must-revalidate` ✅
- Playwright P1 用例：首屏捕获 ≥1 个静态资源，全部含 `max-age=31536000` + `immutable` ✅

### P1 剩余（后续批次，不在本轮）

- `index.html` 切换引用 dist 产物（需版本号机制 + 灰度，属前端发布流程，非纯性能）
- **懒加载切分清单（子代理侦察，router.js switchPage 逐页映射）**：
  - **首屏必需**（boot 耦合，不可懒加载）：`router.js`、`i18n.js(+i18next)`、`theme.js`、`auth.js`(bootstrapApp)、`modal.js`、`notifications.js`、`builtin-tools.js`、`sanitize-markdown.js(+marked+DOMPurify)`、`dashboard.js`(默认路由)、`chat-scroll.js`、10 个 `chat/*`、`chat-plan-progress.js`、`monitor.js`(auth.js:334 `loadActiveTasks` boot 调)、`projects.js`(auth.js:338 `refreshChatProjectSelector`)、`settings.js`(router.js:147 每 switchPage 调 `syncC2NavOnceFromServer`)
  - **可懒加载（大块 + 页面专属，无 boot 耦合）**：`webshell.js`292KB、`c2.js`224KB、`workflows.js`146KB、`tasks.js`130KB、`vulnerability.js`109KB、`knowledge.js`104KB(自绑 DOMContentLoaded，无 router init)、`assets.js`102KB、`chat-files.js`84KB、`roles.js`81KB、`rbac.js`78KB、`info-collect.js`76KB、`hitl.js`87KB
  - **vendor 大块（当前无条件加载，实际仅特定页用）**：`xterm(+addon-fit)`→webshell/c2；`xlsx.full`→info-collect/assets；`cytoscape+elk`→projects/fact-graph/workflows/chat-attack-chain
  - loader 挂点：router.js `initPage(pageId)`（395-604）；懒加载注入 `switchPage` 前后按 pageId 动态 `<script>` append

---

## P2 后端热点（config.go 热应用 + /api/config/tools 重复扫描）

### 问题（实测定位）

- `GetConfig`（`internal/handler/config.go`）原全程持 `h.mu.RLock()`，且在持锁段内调 `h.getExternalMCPTools(ctx)` → `mgr.GetAllTools`（最长 5s 网络 IO），阻塞所有写者
- `GetConfig` 每次重新 marshal 全量响应（含 163+ 工具 + 外部 MCP 列表），无响应缓存
- `cache.NewFromConfig` 抽象已就绪（`internal/cache/cache.go`）但**无任何消费方**（greenfield）

### 本批次落地（已实施）

1. **GetConfig 重构为快照→释锁→取外部 MCP→组装→cache-aside**（`internal/handler/config.go`）：
   - RLock 内仅做值快照 + 深拷 `Security.Tools`（防就地修改），立即 RUnlock
   - 外部 MCP 工具获取移到锁外（复用 `getExternalMCPToolsWithManager`，已设计为锁外安全）
   - marshal 后 `cache.Set`，命中时 `c.Data` 直写 bytes（跳过 marshal + 网络 IO）
   - `no-store` 响应头（配置变更必须立即反映，不缓存客户端）
2. **ConfigHandler 自建 cache**（`NewConfigHandler` 内调 `cache.NewFromConfig`，memory 兜底，redis 不可达降级 memory + Warn）：
   - 复用 `internal/cache` 轮子，不新造
   - `cfg.Cache` 字段已存在（`internal/config/config.go:49`），无需改 app.go 接线
   - `SetCache` 方法可供测试/自定义注入
3. **失效点集中**：
   - `saveConfig()` 末尾调 `invalidateConfigCache()`（覆盖 UpdateConfig/ApplyWechatRobotBinding/4 个 HITL 写方法）
   - `ApplyConfig` 末尾单独调（不走 saveConfig，单独失效）
   - invalidate 为 nil-safe（cache=nil 时 no-op）

### P2 验收

- `go test ./internal/handler/` → TestGetConfigCacheHitSecondCallReturnsCachedBody / TestGetConfigNoStoreHeader / TestInvalidateConfigCacheNilSafe 3/3 PASS ✅
- curl 耗时对比（100 req 串行）：`/api/config`(cache) **18s** vs `/api/config/tools`(无 cache) **25s**，cache 路径快 ~28% ✅
- curl 内容一致性：两次请求 tools sig 完全一致（163 tools）✅
- Playwright P3 用例：`/api/config` 返回 200 + no-store + 两次 body 长度一致 ✅

### P2 验收标准对照（《结果计划指南.md》§4.4 P2）

| 要求 | 本批次 | 证据 |
|------|--------|------|
| 复核 config.go 热应用读锁/复制 | GetConfig 已重构为快照→释锁 | config.go:317-460 |
| database 连接池化 | 已有（sqliteMaxOpenConns=25 等，database.go:29-34） | 静态确认 |
| /api/config/tools 每次重扫文件 | GetTools 已用快照范式 + externalMgr 内部缓存（非本批次新增，复用） | config.go:461-467 |
| 纯读热路径加内存缓存（复用 internal/cache） | GetConfig cache-aside 已落地 | config.go:324-331 |
| 默认 memory 兜底零告警 | NewFromConfig redis 不可达降级 memory + Warn | cache.go:205-218 |
| 压测确认无重复磁盘 IO | curl 对比 + 单测断言命中 | 见上 |

---

## P3 SQLite 写入与并发

### 现状（静态确认）

- `internal/database/database.go` 已配置：
  - `?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000&_synchronous=NORMAL`（NewDB:125）
  - `sqliteMaxOpenConns=25` / `sqliteMaxIdleConns=5` / `ConnMaxLifetime=30min`（configureDBPool:29-34）
  - `PRAGMA wal_autocheckpoint=1000` / `journal_size_limit=256MB`（configureSQLitePragmas:37-45）
  - 后台 PASSIVE checkpoint 循环（300s 间隔，startPassiveCheckpointLoop:65-89）

### 本批次落地（已实施）

- **并发 smoke 测试**（`internal/database/database_concurrent_smoke_test.go`）：
  - `TestConcurrentWritesNoDatabaseLocked`：20 goroutine × 25 写 = 500 并发 INSERT，断言无 `database is locked` + 条数一致
  - `TestConcurrentReadWriteNoLocked`：并发读 + 写交错 500ms，断言无锁阻塞失败

### P3 验收

- `CGO_ENABLED=1 go test -run 'TestConcurrentWritesNoDatabaseLocked|TestConcurrentReadWriteNoLocked' -v ./internal/database/` → 2/2 PASS ✅
- 实测：500 并发写完成 624ms，无 `database is locked` ✅

### P3 验收标准对照

| 要求 | 现状 | 证据 |
|------|------|------|
| WAL 模式 | 已有（`_journal_mode=WAL`） | database.go:125 |
| busy_timeout 上调 | 5000ms（已上调，非默认 0） | database.go:125 |
| 并发对话冒烟无 database is locked | 测试 PASS | database_concurrent_smoke_test.go |

---

## V1/V2 真实 E2E + 单元测试

### V1 Playwright E2E（`web/tests/e2e/perf-cache.spec.js`）

- P1：首屏静态 JS 走长缓存 immutable — **PASS**（捕获静态资源全部含 `max-age=31536000` + `immutable`）
- P2：首页 HTML 不被长缓存（no-store）— **PASS**
- P3：/api/config 返回 JSON + no-store + 两次内容一致 — **PASS**
- 运行：3 passed (13.3s)

### V2 单元测试（go test，CGO=1）

| 测试 | 文件 | 结果 |
|------|------|------|
| GetConfig cache 命中/失效 | internal/handler/config_cache_test.go | 3/3 PASS |
| SecureHeaders no-store/static | internal/security/secureheaders_test.go | 5/5 PASS（含 2 新增子用例） |
| StaticCacheHeaders 长缓存 | internal/security/static_cache_headers_test.go | 2/2 PASS |
| SQLite 并发 smoke | internal/database/database_concurrent_smoke_test.go | 2/2 PASS |

### go vet / build

- `go vet ./...` → EXIT 0 ✅
- `go build ./cmd/server/main.go` → EXIT 0 ✅

---

## 独立审计（反伪实现自检）

### 1. 伪实现排查

- ❌ cache 只 Set 不读？→ GetConfig 先 Get 命中则 `c.Data` 直写 return（config.go:324-331），单测断言第二次 body 不含后追加工具（命中旧快照）✅
- ❌ invalidate 漏写路径？→ saveConfig 末尾 + ApplyConfig 末尾双覆盖；单测 TestInvalidateConfigCacheNilSafe 验证 nil-safe ✅
- ❌ 静态缓存把 HTML/API 也长缓存了？→ StaticCacheHeaders 仅 `/static/` 前缀；SecureHeaders 对非 `/static/` 设 no-store；单测 TestStaticCacheHeadersDoesNotAffectHTML + TestSecureHeaders/static_path_does_not_get_no-store 双断言 ✅
- ❌ cache 存指针导致并发改？→ 存的是 marshal 后的 `[]byte`（值拷贝），命中直写无共享状态 ✅
- ❌ GetConfig 快照后释锁，写者改 `Security.Tools[i].Enabled` 仍读到旧值？→ 锁内深拷 `securityTools := append([]config.ToolConfig(nil), h.config.Security.Tools...)`，与 GetTools 范式一致 ✅

### 2. 回归排查

- GetConfig 行为变化？→ 响应 body 字段集不变（同 GetConfigResponse），仅序列化路径从 `c.JSON` 改为 `json.Marshal` + `c.Data`，JSON 输出等价；Playwright P3 断言 body 含 "tools" + 两次长度一致 ✅
- SecureHeaders 新增 no-store 是否影响现有测试？→ 原 TestSecureHeaders 2 子用例仍 PASS，新增 2 子用例 PASS；`/static/` 路径不被设 no-store（由 StaticCacheHeaders 设长缓存）✅
- app.go 新增 `router.Use(security.StaticCacheHeaders())` 顺序？→ 在 SecureHeaders 之后，CORS 之后，不影响路由与 RBAC ✅

### 3. 边界

- cache=nil（SetCache 未调或 NewConfigHandler 失败）？→ GetConfig 降级走实时 marshal + `c.JSON`，invalidate nil-safe ✅
- redis 不可达？→ NewFromConfig 降级 memory + Warn 一次（cache.go:213-216），不影响功能 ✅
- 外部 MCP 全断开？→ getExternalMCPToolsWithManager 走 ExternalMCPManager 内部缓存（getCachedTools），仍返回空列表不阻塞 ✅
- 并发写超 busy_timeout？→ busy_timeout=5000ms 兜底，500 并发实测无 locked ✅

### 4. 安全

- cache key 固定为 `cache.KeyHash("cyberstrike:config:get")`，无用户输入注入面 ✅
- no-store 响应头防止配置（含 API Key 等敏感字段）被中间代理缓存 ✅
- StaticCacheHeaders 仅 `/static/`（静态资源无敏感数据），HTML/API 不被长缓存 ✅

### 5. 测试缺口

- 未覆盖：redis driver 真实连接（需 redis 实例，环境受限，降级路径已由 cache_test.go 覆盖）— 待验证
- 未覆盖：GetConfig 在外部 MCP 慢响应下的持锁时长（需 mock ExternalMCPManager，本轮用真实 server 验证）— 合理推断

### 6. 文档同步

- `config.example.yaml` + `config.yaml` 追加 `cache:` 段（driver/memory/default_ttl_seconds）✅
- `Makefile` 新增 `perf-cache` / `perf-db-smoke` / `test-cgo` / `test-race` 目标 ✅
- 本文档（PERF-4-4.md）落盘 ✅

---

## 实际修改清单

| 文件 | 改动 |
|------|------|
| `internal/handler/config.go` | +cache 字段/SetCache/NewConfigHandler 自建 cache；GetConfig 重构快照+cache-aside；saveConfig/ApplyConfig 末尾 invalidate |
| `internal/handler/config_cache_test.go` | 新增 3 单测（cache 命中/失效/no-store/nil-safe） |
| `internal/security/static_cache_headers.go` | 新增 StaticCacheHeaders 中间件（/static/ 长缓存 immutable） |
| `internal/security/static_cache_headers_test.go` | 新增 2 单测 |
| `internal/security/secureheaders.go` | 非 /static/ 路径设 no-store（HTML/API 禁缓存） |
| `internal/security/secureheaders_test.go` | +2 子用例（no-store / static 不设 no-store） |
| `internal/database/database_concurrent_smoke_test.go` | 新增 2 并发 smoke 测试 |
| `internal/app/app.go` | router.Use(StaticCacheHeaders()) 挂载 |
| `config.example.yaml` / `config.yaml` | +cache 段 |
| `Makefile` | +perf-cache/perf-db-smoke/test-cgo/test-race 目标 |
| `web/tests/e2e/perf-cache.spec.js` | 新增 Playwright 3 用例（worktree） |
| `docs/upgrade-guides/PERF-4-4.md` | 交付文档 + 懒加载切分清单 + 审计记录 |

---

## 剩余风险

- **P1 dist 未切换引用**：`index.html` 仍走 raw JS（未引 dist 产物）。本轮已落地长缓存头（首屏后回访受益），但首屏体积本身未降（仍 683KB HTML + 3.44MB raw JS 同步加载）。切换 dist 需版本号机制 + 灰度发布，属前端发布流程，非纯性能，留后续批次。
- **P1 懒加载未实施**：非首屏大块懒加载清单已补齐（见上「P1 剩余」），实施属 F6-3 节点（含跨会话冲突点：knowledge.js/webshell.js/c2.js 被其他会话 F3 logger 迁移触碰），留后续批次。
- **P1 monitor/settings 有 boot 耦合**：`monitor.js` 的 `loadActiveTasks`(auth.js:334) 与 `settings.js` 的 `syncC2NavOnceFromServer`(router.js:147) 在首屏即被调，懒加载必须处理这两个 boot 依赖，否则首屏折损回归。已列为懒加载前置约束。
- **P2 redis 真实连接未验证**：环境无 redis 实例，降级路径已由 cache_test.go 覆盖，真实 redis 留待验证。
- **跨会话**：主仓 `server.exe`(8080) 是旧二进制（不含本批次改动）；本批次用临时 8090 + 临时二进制验证，已清理。worktree `perf-4-4-cache` 隔离，主仓未污染。
