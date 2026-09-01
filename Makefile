.PHONY: build vet test clean skills-lock verbs-gate

build:
	go build -o cyberstrike-ai.exe cmd/server/main.go

vet:
	go vet ./...

test:
	go test ./... -count=1

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

