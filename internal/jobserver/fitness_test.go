package jobserver

import (
	"bufio"
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
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
