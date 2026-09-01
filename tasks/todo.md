# tasks/todo.md — I 批次任务清单（spec.md 契约）

> 依赖顺序：I1/I2/I3/I4/I5 并行 → I6（依赖 I3 完成避免 chat.js 冲突）→ I7（全部完成后统一 E2E+审查）→ I8 → I9

- [x] I0: spec.md 落盘（六要素：Objective/Stack/Commands/Structure/Style/Testing/Boundaries/Criteria）
  - Verify: 文件存在且覆盖六核心域
- [ ] I1: 确定性安全五闸（shellsafe/HIGH_IMPACT/scope/TurnLimiter/tool_call_ids）
  - Accept: 22+ 注入 case 拦截；高危工具标记；scope 14 case；单 turn 限流生效
  - Verify: go test ./internal/security/ -run "TestShellSafe|TestHighImpact|TestScope" + ./internal/multiagent/ -run TestTurnLimiter；go vet/build
  - Files: internal/security/shellsafe.go+test, highimpact.go+test, scope.go+test, executor.go 接线, config.go, internal/multiagent/eino_turn_limiter.go+test
- [ ] I2: skill 供应链双闸（skills-lock.json + verbs-gate）
  - Accept: 锁覆盖全部 skill（SHA256）；Verify 三型违规；幽灵工具清单产出；CI job
  - Verify: go test ./internal/skillpackage/ -v；skills-lock.json 落盘；verbs-gate 实跑报告
  - Files: internal/skillpackage/lock.go+test, verbs_gate.go+test, skills-lock.json, cmd/verbs-gate/, .github/workflows
- [ ] I3: Cache-Aside（memory+Redis 可选）+ 8 无线工具 yaml
  - Accept: -race 过；redis 假地址降级 memory；/api/config/tools 含 aircrack 等 8 个
  - Verify: go test ./internal/cache/ -race；启动冒烟 curl
  - Files: internal/cache/cache.go+test, config.go, config.example.yaml, tools/wireless/*.yaml×8, go.mod
- [ ] I4: Electron 原生外壳（托盘/启动画面/错误对话框/单实例锁）
  - Accept: 关窗最小化到托盘；splash 三阶段状态；后端异常弹原生对话框；双开被拒
  - Verify: node --check ×all + desktop/test/smoke.js exit 0
  - Files: desktop/src/tray.js, splash.html, main.js, test/smoke.js
- [ ] I5: 文档体系（ADR×6/SOP/ONBOARDING/README 门面）
  - Accept: 每篇 ADR 含备选方案对比；SOP 覆盖开发/发布/回滚/排障/授权；事实核实
  - Verify: 引用 file:line 抽查 + 链接有效
  - Files: docs/adr/, docs/SOP.md, docs/ONBOARDING.md, README.md, docs/README.md
- [ ] I6: 前端模块化（chat.js 11190 行拆分）
  - Accept: 拆分后 node --check 全过、index.html script 引用更新、行为不变
  - Verify: node --check + 冒烟（对话页加载/SSE 渲染）
  - Files: web/static/js/chat/（模块产物）, web/templates/index.html
- [ ] I7: 全链路 E2E + 独立 Critic + 修复循环
  - Accept: I 批次全部验收点过；Critic 无未关闭 Blocking
  - Verify: 启动→免登录→对话→工具→审批标记→限流→安全头→skills-lock→无线工具 11 项 curl 实测
- [ ] I8: 提交推送 + NSIS + Release
  - Accept: main 提交推送；安装包含 I 批次；Release state=uploaded
- [ ] I9: HTML 变更报告（含 5-10 题测验）+ 记忆/规则沉淀（验证台账复用）
  - Accept: 报告自包含无 CDN；测验交互可用；验证记录落 docs/verification-ledger.md
