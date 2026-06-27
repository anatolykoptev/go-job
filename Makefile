BINARY = bin/go_job
SERVICE = go-job

.PHONY: build deploy restart clean lint preflight

build:
	GOWORK=off go build -o $(BINARY) .

deploy: build
	cp deploy/go_job.service $(HOME)/.config/systemd/user/$(SERVICE).service
	systemctl --user daemon-reload
	systemctl --user restart $(SERVICE)
	@echo "Deployed and restarted $(SERVICE)"

restart:
	systemctl --user restart $(SERVICE)

lint:
	GOWORK=off golangci-lint run ./...

clean:
	rm -f $(BINARY)

# preflight — the merge gate. Scoped to the two package trees that carry
# DB round-trip tests; -p 1 caps parallelism for the 4-core ARM box.
# With DATABASE_URL set (e.g. CI ephemeral postgres), the previously
# t.Skip'd DB round-trip tests run live. Without it they skip cleanly.
# go vet runs on the same two trees — not ./... (avoids the full workspace
# on a prod box).
preflight:
	@echo "==> go vet ./internal/adminui/... ./internal/engine/jobs/..."
	GOWORK=off go vet ./internal/adminui/... ./internal/engine/jobs/...
	@echo "==> go test -p 1 ./internal/adminui/... ./internal/engine/jobs/..."
	GOWORK=off go test -p 1 ./internal/adminui/... ./internal/engine/jobs/...
