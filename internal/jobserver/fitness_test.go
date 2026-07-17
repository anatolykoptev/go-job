package jobserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolCountCeiling verifies the number of registered tools does not exceed the ceiling.
// This guards against re-sprawl. Parse register.go and count registerXxx(server) call lines.
func TestToolCountCeiling(t *testing.T) {
	const ceiling = 30 // headroom: current=28 (52→28 reduction); bump only when intentionally adding tools

	f, err := os.Open("register.go")
	if err != nil {
		t.Fatalf("cannot open register.go: %v", err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "register") && strings.Contains(line, "(server)") {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan error: %v", err)
	}

	if count > ceiling {
		t.Errorf("tool count %d exceeds ceiling %d — bump ceiling only when intentionally adding tools", count, ceiling)
	}
	t.Logf("registered tools: %d (ceiling: %d)", count, ceiling)
}

// TestHuntWriterInvariant verifies the engine's hunt-writer symbols still exist.
func TestHuntWriterInvariant(t *testing.T) {
	data, err := os.ReadFile("../engine/jobs/opportunity_search.go")
	if err != nil {
		t.Fatalf("cannot read opportunity_search.go: %v", err)
	}

	for _, sym := range []string{"func PersistBounties(", "func PersistSecurity(", "func PersistFreelanceJobs("} {
		if !bytes.Contains(data, []byte(sym)) {
			t.Errorf("hunt-writer invariant BROKEN: %s missing from opportunity_search.go", sym)
		}
	}
	t.Log("hunt-writer invariant: all symbols present")
}

// TestNoGodTool verifies no consolidated input struct exceeds maxFields.
func TestNoGodTool(t *testing.T) {
	const maxFields = 13

	cases := []struct {
		name string
		val  any
	}{
		{"huntListInput", huntListInput{}},
		{"jobTrackerInput", jobTrackerInput{}},
		{"linkedInInput", linkedInInput{}},
		{"oversizeInput", oversizeInput{}},
		{"resumeMemoryInput", resumeMemoryInput{}},
		{"researchInput", researchInput{}},
		{"atsInput", atsInput{}},
	}
	for _, c := range cases {
		n := reflect.TypeOf(c.val).NumField()
		if n > maxFields {
			t.Errorf("%s has %d fields (max %d) — too many params for a single tool", c.name, n, maxFields)
		}
		t.Logf("%s: %d fields", c.name, n)
	}
}

// TestNoBooleanPropertySchema verifies that no tool's inputSchema or outputSchema
// has a property value that is a JSON boolean (true/false).
//
// The go-sdk reflects `any`-typed fields as the boolean JSON Schema `true` when the
// struct is walked via reflection. Claude Code's MCP client rejects a boolean where
// it expects an object schema — for example:
//
//	"properties": { "entries": true }   // INVALID: Claude Code rejects this
//	"properties": { "entries": {...} }  // VALID
//
// This test creates a real MCP server, registers all tools, calls tools/list
// through an in-memory transport, and fails if any tool's schema contains a boolean
// property. It will FAIL if huntListOutput.Entries (or any other field) is reverted
// back to `any`.
func TestNoBooleanPropertySchema(t *testing.T) {
	// Build the server exactly as main.go does.
	srv := mcp.NewServer(&mcp.Implementation{Name: "go_job-test", Version: "test"}, nil)
	RegisterTools(srv, applications.New(nil, ""))

	// Connect via in-memory transport.
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	result, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	t.Logf("checking %d tools for boolean property schemas", len(result.Tools))

	for _, tool := range result.Tools {
		checkSchemaForBooleanProperties(t, tool.Name, "inputSchema", tool.InputSchema)
		if tool.OutputSchema != nil {
			checkSchemaForBooleanProperties(t, tool.Name, "outputSchema", tool.OutputSchema)
		}
	}
}

// checkSchemaForBooleanProperties walks a JSON Schema value and fails the test
// if any property under "properties" is a boolean rather than an object.
// The key "additionalProperties" is excluded because boolean false is a valid
// and common JSON Schema pattern for that keyword.
func checkSchemaForBooleanProperties(t *testing.T, toolName, schemaKind string, schema any) {
	t.Helper()

	// Re-marshal and parse as map so we can walk it generically regardless of
	// what concrete type the SDK stored in InputSchema / OutputSchema.
	b, err := json.Marshal(schema)
	if err != nil {
		t.Errorf("tool %q %s: cannot marshal schema: %v", toolName, schemaKind, err)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Errorf("tool %q %s: cannot unmarshal schema as object: %v", toolName, schemaKind, err)
		return
	}
	walkSchemaProperties(t, toolName, schemaKind, m, "")
}

// walkSchemaProperties recursively walks a schema object and reports any
// property value that is a JSON boolean.
func walkSchemaProperties(t *testing.T, toolName, schemaKind string, schema map[string]any, path string) {
	t.Helper()

	props, ok := schema["properties"]
	if !ok {
		return
	}
	propsMap, ok := props.(map[string]any)
	if !ok {
		return
	}
	for name, val := range propsMap {
		if name == "additionalProperties" {
			// boolean false is a valid and common JSON Schema idiom for additionalProperties.
			continue
		}
		fieldPath := path
		if fieldPath != "" {
			fieldPath += "."
		}
		fieldPath += name

		switch v := val.(type) {
		case bool:
			t.Errorf("tool %q %s: properties.%s is a boolean (%v) — the go-sdk reflects `any`-typed fields as boolean true; change the field type to a concrete type",
				toolName, schemaKind, fieldPath, v)
		case map[string]any:
			// Recurse into nested object schemas.
			walkSchemaProperties(t, toolName, schemaKind, v, fieldPath)
		}
	}
}

// TestFF1_RegistryCompleteness is the P1 fitness function: every advertised
// platform= value resolves to at least one registered Source, every registered
// source has at least one group, and meta-groups are non-empty.
// Fails CI if a source is added to the const block without a Register() call.
func TestFF1_RegistryCompleteness(t *testing.T) {
	advertised := []string{
		platLinkedIn, platGreenhouse, platLever, platAshby,
		platATS, platYC, platHN, platIndeed, platHabr, platTwitter,
		platGoogle, platStartup, platCraigslist, platRemoteOK, platWWR,
		platRemotive, platRemote, platFreelancer, platInspira, platUNDP, platUN,
		platAll,
	}
	for _, p := range advertised {
		srcs := jobRegistry.Select(p)
		if len(srcs) == 0 {
			t.Errorf("FF-1: advertised platform=%q routes to NO registered source — registry completeness BROKEN", p)
		}
	}
	for _, s := range jobRegistry.All() {
		if len(s.Groups()) == 0 {
			t.Errorf("FF-1: registered source %q has no groups — unreachable via any Select()", s.Name())
		}
	}
	t.Logf("FF-1 PASS: %d advertised platforms checked, %d sources registered", len(advertised), len(jobRegistry.All()))
}

// TestFF2_PanicIsolation asserts that a panicking Source does not propagate —
// the panicking source yields 0 results + a non-nil error.
func TestFF2_PanicIsolation(t *testing.T) {
	ch := make(chan sourceResult, 2)
	ctx := context.Background()

	// Panicking source.
	panicSrc := &testPanicSource{}
	go runSource(ctx, panicSrc, connectors.Query{Query: "test"}, ch)
	r := <-ch
	if r.err == nil {
		t.Error("FF-2: panicking source must produce non-nil error")
	}
	if len(r.results) != 0 {
		t.Errorf("FF-2: panicking source must produce 0 results, got %d", len(r.results))
	}
	t.Logf("FF-2 PASS: panic isolated, err=%v", r.err)
}

// testPanicSource is a test double that always panics in Fetch.
type testPanicSource struct{}

func (testPanicSource) Name() string                        { return "test-panic" }
func (testPanicSource) Capabilities() connectors.Capability { return 0 }
func (testPanicSource) Groups() []string                    { return []string{"all"} }
func (testPanicSource) SiteScope() string                   { return "" }
func (testPanicSource) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	panic("test panic")
}

// testSlowSource sleeps until its context is cancelled.
type testSlowSource struct{ sleep time.Duration }

func (testSlowSource) Name() string                        { return "test-slow" }
func (testSlowSource) Capabilities() connectors.Capability { return 0 }
func (testSlowSource) Groups() []string                    { return []string{"all"} }
func (testSlowSource) SiteScope() string                   { return "" }
func (s testSlowSource) Fetch(ctx context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	select {
	case <-time.After(s.sleep):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestFF3_PerSourceDeadlineBound asserts that a slow source is bounded by its
// context deadline and classified as outcome=timeout. The fan-out completes
// within a reasonable wall-time budget.
//
// Revert-red: removing the per-source context propagation (i.e. passing a
// non-cancellable context) would cause this test to time out or the outcome
// would never be "timeout".
func TestFF3_PerSourceDeadlineBound(t *testing.T) {
	ch := make(chan sourceResult, 1)

	// Context that expires quickly — the slow source will hit it.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	slow := testSlowSource{sleep: 10 * time.Second}
	go runSource(ctx, slow, connectors.Query{Query: "test"}, ch)

	select {
	case r := <-ch:
		if r.err == nil {
			t.Fatal("FF-3: slow source should have returned a context error")
		}
		outcome := engine.PlatformOutcome(len(r.results), r.err)
		if outcome != "timeout" {
			t.Errorf("FF-3: slow source outcome = %q, want %q (err: %v)", outcome, "timeout", r.err)
		}
		t.Logf("FF-3 PASS: slow source bounded, outcome=%s, err=%v", outcome, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("FF-3: fan-out did not complete within 2s — per-source deadline not enforced")
	}
}

// TestFF3b_PerSourceTimeout_IndependentFromParent — the load-bearing test for
// the per-source timeout added in P4. This test proves the per-source cap fires
// even when the PARENT context has a LONG deadline. The existing FF-3 test
// (TestFF3_PerSourceDeadlineBound) only covers parent cancellation; it passes
// even WITHOUT a per-source timeout in runSource (the parent ctx at 50ms would
// cancel the source regardless). This test requires an independent cap.
//
// Revert-red: removing context.WithTimeout(ctx, perSourceTimeout) from runSource
// causes this test to timeout waiting 2s — the slow source is NOT cancelled by
// the parent (10s deadline >> test window), only the per-source cap would cancel it.
//
// BH-16: This test mutates the package-level perSourceTimeout var. It does NOT
// call t.Parallel() — the save/restore pattern is safe only when tests run
// sequentially within the package (the Go default). Do NOT add t.Parallel()
// here without first converting perSourceTimeout to atomic.Value.
func TestFF3b_PerSourceTimeout_IndependentFromParent(t *testing.T) {
	prevTimeout := perSourceTimeout
	perSourceTimeout = 50 * time.Millisecond
	defer func() { perSourceTimeout = prevTimeout }()

	ch := make(chan sourceResult, 1)

	// Parent has a LONG (10s) deadline — it must NOT be the one that bounds the slow source.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slow := testSlowSource{sleep: 30 * time.Second}
	go runSource(ctx, slow, connectors.Query{Query: "test"}, ch)

	select {
	case r := <-ch:
		outcome := engine.PlatformOutcome(len(r.results), r.err)
		if outcome != "timeout" {
			t.Errorf("FF-3b: expected outcome=timeout from per-source cap, got %q (err: %v)", outcome, r.err)
		}
		t.Logf("FF-3b PASS: per-source timeout fired independently, outcome=%s", outcome)
	case <-time.After(2 * time.Second):
		t.Fatal("FF-3b: per-source timeout did not fire within 2s — " +
			"perSourceTimeout not applied in runSource (revert the WithTimeout call)")
	}
}

// TestFF4_SentinelErrors_ClassifiedViaHooks verifies that after initJobRegistry
// wires the hooks, errors.Is(err, jobs.ErrNoAPIKey) → outcome=no_key and
// errors.Is(err, jobs.ErrParse) → outcome=parse_fail.
//
// Revert-red: removing the RegisterPlatformOutcomeHooks call from initJobRegistry
// causes no_key/parse_fail to fall through to "error".
func TestFF4_SentinelErrors_ClassifiedViaHooks(t *testing.T) {
	// initJobRegistry is called by TestMain so hooks are already wired.
	noKeyWrapped := wrapErr("indeed: no api key", jobs.ErrNoAPIKey)
	parseWrapped := errors.Join(jobs.ErrParse, errors.New("json decode"))

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ErrNoAPIKey direct", jobs.ErrNoAPIKey, "no_key"},
		{"ErrNoAPIKey wrapped", noKeyWrapped, "no_key"},
		{"ErrParse direct", jobs.ErrParse, "parse_fail"},
		{"ErrParse wrapped", parseWrapped, "parse_fail"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.PlatformOutcome(0, tc.err)
			if got != tc.want {
				t.Errorf("PlatformOutcome(0, %v) = %q, want %q — hooks not wired correctly?", tc.err, got, tc.want)
			}
		})
	}
}

// wrapErr creates a wrapped error for test use without importing "fmt" and
// risking a shadow of the stdlib package name.
func wrapErr(msg string, cause error) error {
	return errors.Join(errors.New(msg), cause)
}
