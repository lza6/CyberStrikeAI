.PHONY: build vet test clean skills-lock verbs-gate test-race cover perf-cache perf-db-smoke

build:
	go build -o cyberstrike-ai.exe cmd/server/main.go

vet:
	go vet ./...

# K3：测试带 race 检测（golang/rules/golang/testing.md 要求）。无 CGO 时回退普通 test。
test:
	go test ./... -count=1

test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

# K3：覆盖率（移植自 agent-orchestrator coverage.yml diff-cover 80% 门禁）。
# 生成 cover.out + HTML 报告；CI 可用 diff-cover 做 PR 差异覆盖率门禁。
cover:
	go test -coverprofile=cover.out -count=1 ./internal/...
	go tool cover -func=cover.out | tail -1
	@echo "cover.out 已生成，HTML 报告：go tool cover -html=cover.out"

clean:
	rm -f cyberstrike-ai.exe

# 生成 skill 供应链锁（SHA256）；改了 skills/ 后跑这个刷新
skills-lock:
	go run cmd/genlock/main.go

# 扫描 skill 工具引用漂移（report 模式 exit 0）；-strict 发现幽灵 exit 1（CI 门禁）
verbs-gate:
	go run cmd/verbs-gate/main.go

verbs-gate-strict:
	go run cmd/verbs-gate/main.go -strict


# P2 后端热点：/api/config cache-aside 行为验证（命中 vs 失效）
perf-cache:
	CGO_ENABLED=1 go test -run 'TestGetConfigCache|TestGetConfigNoStore|TestInvalidateConfigCacheNilSafe' -count=1 -v ./internal/handler/

# P3 SQLite 并发写冒烟：验证无 "database is locked"（WAL + busy_timeout）
perf-db-smoke:
	CGO_ENABLED=1 go test -run 'TestConcurrentWritesNoDatabaseLocked|TestConcurrentReadWriteNoLocked' -count=1 -v ./internal/database/
