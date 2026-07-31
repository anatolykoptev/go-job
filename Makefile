BINARY = bin/go_job
SERVICE = go-job
# Test parallelism: -p 1 on the 4-core prod box (contention inflates per-test
# times ~30x); CI overrides via GO_TEST_PARALLEL=2 on GitHub-hosted runners.
GO_TEST_PARALLEL ?= 1

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

# preflight — the merge gate. GO_TEST_PARALLEL caps test parallelism (default
# -p 1 for the 4-core ARM prod box; CI sets GO_TEST_PARALLEL=2 on GitHub-hosted
# runners with 4 cores and no contention).
# With DATABASE_URL set (e.g. CI ephemeral postgres), the previously
# t.Skip'd DB round-trip tests run live. Without it they skip cleanly.
# go vet and go test run on ./internal/... (all internal packages).
preflight:
	@echo "==> fitness: capped response readers must detect truncation"
	@if grep -R -n --include='*.go' 'io\.ReadAll(io\.LimitReader(' internal/engine/jobs internal/hunt/discovery | grep -vE '(errors|bodylimit|_test|algora_attempt|github_prs)\.go:'; then echo "FAIL: bare capped reader silently truncates -- use readLimitedBody"; exit 1; fi
	@echo "==> fitness: no inline math.Round*100 clone in resume_edit.go (use parseDollarsToCents)"
	@! grep -n 'math\.Round.*\*[[:space:]]*100' internal/adminui/resume_edit.go || (echo "FAIL: inline math.Round*100 cents parse found in resume_edit.go -- use parseDollarsToCents" && exit 1)
	@echo "==> fitness: no inline Sprintf dollar-display+/hr in upwork.go (use centsToDollars)"
	@! grep -n 'Sprintf.*\$.*\.2f.*/hr' internal/adminui/upwork.go || (echo "FAIL: inline Sprintf dollar-display+/hr found in upwork.go -- use centsToDollars" && exit 1)
	@echo "==> person-scope fitness: new upwork SQL consts verified by TestNewSQLConstants_Structure test"
	@grep -q "insertUpworkCatalogItemSQL" internal/engine/jobs/upwork_profile.go && grep -q "deleteUpworkCatalogItemSQL" internal/engine/jobs/upwork_profile.go && echo "OK: upwork catalog SQL consts present" || (echo "FAIL: upwork catalog SQL consts missing"; exit 1)
	@echo "==> single-CSS-site fitness: copy-block/char-chip CSS rules live ONLY in partials.go (sharedCSS), never regrown in linkedin.go/upwork.go"
	@! grep -nE '\.(gd-copy-btn|li-pre|li-code-wrap|cc-muted|cc-green|cc-amber|cc-red)\{' internal/adminui/linkedin.go internal/adminui/upwork.go || (echo "FAIL: copy-block/char-chip CSS rule regrew in linkedin.go/upwork.go -- it must live only in partials.go sharedCSS" && exit 1)
	@grep -qE '\.gd-copy-btn\{' internal/adminui/partials.go || (echo "FAIL: copy-block CSS missing from partials.go sharedCSS -- the single source of truth was removed" && exit 1)
	@echo "OK: copy-block/char-chip CSS is single-sourced in partials.go"
	@echo "==> go vet ./internal/..."
	GOWORK=off go vet ./internal/...
	@echo "==> go test -p $(GO_TEST_PARALLEL) ./internal/..."
	GOWORK=off go test -p $(GO_TEST_PARALLEL) ./internal/...
