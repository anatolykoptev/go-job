package jobs

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestFF_FetchVacancy_NoDirectNetHTTP is the arch fitness function:
// fetchone.go must NOT import "net/http" for outbound job-board fetches.
// It should only call fetchRenderedHTML (the go-wowa render seam).
// This prevents future drift into a parallel fetch path.
func TestFF_FetchVacancy_NoDirectNetHTTP(t *testing.T) {
	f, err := os.Open("fetchone.go")
	if err != nil {
		t.Fatalf("cannot open fetchone.go: %v", err)
	}
	defer f.Close()

	// Scan all import lines — any "net/http" import in this file is a violation.
	// The go-wowa seam (fetchRenderedHTML) owns the HTTP call; fetchone.go must
	// not bypass it with a raw http.Get or http.NewRequest.
	inImports := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "import (" {
			inImports = true
			continue
		}
		if inImports && line == ")" {
			inImports = false
			continue
		}
		if inImports && strings.Contains(line, `"net/http"`) {
			t.Errorf("fetchone.go imports net/http directly -- use fetchRenderedHTML instead; found line: %s", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	t.Log("FF_NoDirectNetHTTP PASS: fetchone.go does not import net/http")
}
