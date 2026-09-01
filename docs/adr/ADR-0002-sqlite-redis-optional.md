# ADR-0002 SQLite 作为主存储，Redis 作可选缓存

**状态**：accepted  
**日期**：2026-09

## 背景

平台需持久化对话/漏洞/资产/攻击链/知识库向量。存储选型需平衡：单文件桌面定位、生产级并发写入、向量检索、运维复杂度。

## 决策

**主存储用 SQLite（mattn/go-sqlite3 CGO + WAL + schema_migrations 版本表），Redis 作为可选缓存层（默认关闭，启用时 Cache-Aside 降 DB 负载）。**

## 备选方案对比

| 方案 | 优点 | 劣势 |
|------|------|------|
| **SQLite + Redis 可选（选定）** | 单文件、CGO 集成、WAL 并发写、零外部依赖（Redis 关时）、生产桌面两栖 | 向量检索需自建或用 sqlite-vec；高并发写有锁竞争 |
| Postgres + pgvector + Redis | 向量原生、高并发、复制 | 破坏单文件桌面定位、运维重、新手起不来 |
| 纯 SQLite（无 Redis） | 最简 | 高 QPS 读场景 DB 负载高 |

## 后果

- **正面**：`data/conversations.db` 单文件可备份复制；Redis `cache.driver: redis` 配置即开，连接失败自动降级 memory + Warn（零额外告警）；Cache-Aside 缓存 LLM 解析结果/列表读取，重复查询不烧 token。
- **负面**：向量检索用 SQLite 而非专用向量库，大规模知识库（>10万条）检索性能弱于 pgvector——用 memory 兜底 + 限制知识库规模缓解。
- **迁移路径**：若未来生产环境需 pgvector，可在 `cache.go` 之上加 `VectorStore` 接口，SQLite/Postgres 双实现，不改上层。
