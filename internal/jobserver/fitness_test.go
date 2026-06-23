package jobserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
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

	for _, sym := range []string{"persistBounties", "persistSecurity", "persistFreelanceJobs"} {
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
	RegisterTools(srv)

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

func (testPanicSource) Name() string                    { return "test-panic" }
func (testPanicSource) Capabilities() connectors.Capability { return 0 }
func (testPanicSource) Groups() []string                { return []string{"all"} }
func (testPanicSource) SiteScope() string               { return "" }
func (testPanicSource) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	panic("test panic")
}
