.PHONY: build vet test clean

build:
	go build -o cyberstrike-ai.exe cmd/server/main.go

vet:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f cyberstrike-ai.exe
