# CyberStrikeAI 前端 E2E 测试

## 运行
1. 启动后端：`./cyberstrike-ai.exe -config config.yaml --http`（config.yaml 需 `auth.local_mode: true`）
2. `cd web/tests/e2e && npm install && npx playwright test`

## 覆盖
- 首屏加载 + 免登录进对话页
- 系统设置页区块（AI 通道/系统提示词/版本更新）
- 攻击剧本页卡片
