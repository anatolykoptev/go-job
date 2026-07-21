.RECIPEPREFIX = >

.PHONY: test lint preflight

test:
> @GOWORK=off go test ./...

lint:
> @GOWORK=off golangci-lint run --allow-parallel-runners ./...

# Merge-gate for .github/workflows/preflight.yml: gofmt + vet + build + test.
# GOWORK=off so a local run on krolik (which has a repo-spanning go.work) builds
# this module in isolation; a no-op on the hosted CI runner.
preflight:
> @echo "==> gofmt -l"
> @out=$$(GOWORK=off gofmt -l .); \
> if [ -n "$$out" ]; then \
> echo "FAIL: gofmt drift (run: gofmt -w <file>):"; \
> echo "$$out"; \
> exit 1; \
> fi
> @echo "==> go vet ./..."
> @GOWORK=off go vet ./...
> @echo "==> go build ./..."
> @GOWORK=off go build ./...
> @echo "==> go test ./..."
> @GOWORK=off go test ./...
