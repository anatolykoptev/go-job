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
	@echo "==> identity fitness: tenant.From is never an account-identity source (ADR-1; its global 'spb' default makes it fail-open)"
	@! grep -rn 'tenant\.From' internal/accounts/ internal/adminui/ main.go || (echo "FAIL: tenant.From used in adminui/accounts/main -- account identity must come from auth.SessionFrom / panelmcp.TenantResolver, never the global-default tenant accessor" && exit 1)
	@echo "==> identity fitness: zero RequiredRole decls in adminui (HMAC rollback is not a RoleAuthenticator; Register panics fail-closed, ADR-17)"
	@! grep -rn 'RequiredRole:[[:space:]]*"' internal/adminui/ --include='*.go' | grep -v '_test\.go' || (echo "FAIL: RequiredRole declared on an adminui resource -- under AUTH_DRIVER=hmac this panics at Register; role gating is bcrypt-only and unused in v1" && exit 1)
	@echo "==> identity fitness: X-MCP-User edge header is never read (post-Caddy-exemption it is client-controllable, ADR-4)"
	@! grep -rn 'X-MCP-User' internal/ main.go --include='*.go' | grep -v '_test\.go' || (echo "FAIL: X-MCP-User consumed -- after the Caddy exemption this header is attacker-controlled; identity comes from verified bearer keys only" && exit 1)
	@echo "==> identity fitness: ADR-6 boot order (EnsureSchema -> role migration -> mcp_api_keys DDL -> seed) is asserted by accounts.TestBootstrap_Order_SourceGate; Bootstrap-before-hStore.Migrate by accounts.TestBootstrap_PrecedesHuntMigrate_SourceGate"

	@echo "==> go vet ./internal/..."
	GOWORK=off go vet ./internal/...
	@echo "==> go test -p $(GO_TEST_PARALLEL) ./internal/..."
	GOWORK=off go test -p $(GO_TEST_PARALLEL) ./internal/...

# mutation: run gremlins mutation testing on PR-diff changes.
# Requires gremlins installed (https://gremlins.dev/latest/install/).
# Usage:
#   make mutation                      # diff against origin/main
#   make mutation DIFF=abc123          # diff against specific commit
#   make mutation DIFF=origin/main DRY_RUN=1  # dry-run (list candidates, no test)
mutation:
	@echo "==> gremlins mutation testing (diff: $(or $(DIFF),origin/main))"
	GOWORK=off gremlins unleash \
		--diff "$(or $(DIFF),origin/main)" \
		--exclude-files "^vendor/" \
		--exclude-files "^cmd/" \
		--exclude-files "_test\.go$$" \
		--exclude-files "_gen\.go$$" \
		$(if $(DRY_RUN),--dry-run) \
		--output-statuses "lc"
	@echo "==> mutation testing complete"
