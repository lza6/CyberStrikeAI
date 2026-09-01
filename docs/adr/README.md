# CyberStrikeAI 架构决策记录（ADR）

本目录记录 CyberStrikeAI 的关键架构决策：背景、决策、备选方案、后果、状态。

## 索引

- [ADR-0001 选择 Go + Eino ADK 作为 Agent 编排](ADR-0001-go-eino-adk.md)
- [ADR-0002 SQLite 作为主存储，Redis 作可选缓存](ADR-0002-sqlite-redis-optional.md)
- [ADR-0003 MCP 作为工具协议 + 本地工具 yaml 双轨](ADR-0003-mcp-yaml-dual-track.md)
- [ADR-0004 local_mode 免登录模式与安全边界](ADR-0004-local-mode-safety.md)
- [ADR-0005 Electron 壳 + Web UI 主体](ADR-0005-electron-web-shell.md)
- [ADR-0006 确定性安全层叠在 LLM 之前](ADR-0006-deterministic-safety-layer.md)
