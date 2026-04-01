.PHONY: build test lint clean

build:
	GOWORK=off go build ./...

test:
	GOWORK=off go test -race -count=1 ./...

lint:
	GOWORK=off golangci-lint run ./...

clean:
	go clean -cache -testcache
