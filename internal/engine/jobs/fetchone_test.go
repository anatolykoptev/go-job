package jobs

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestFF_FetchVacancy_NoDirectNetHTTP is the arch fitness function:
// fetchone.go and tool_vacancy_ingest.go must NOT import "net/http" directly
// for outbound job-board fetches. The go-wowa seam (fetchRenderedHTML) owns
// the HTTP call; bypassing it with http.Get / http.NewRequest is a violation.
// Catches multiline imports, single-line imports, and aliased imports.
func TestFF_FetchVacancy_NoDirectNetHTTP(t *testing.T) {
	for _, filename := range []string{"fetchone.go", "../jobserver/tool_vacancy_ingest.go"} {
		t.Run(filename, func(t *testing.T) {
			f, err := os.Open(filename)
			if err != nil {
				// tool_vacancy_ingest.go lives in a sibling package; skip if relative path fails
				if filename != "fetchone.go" {
					t.Skipf("cannot open %s (sibling package): %v", filename, err)
				}
				t.Fatalf("cannot open %s: %v", filename, err)
			}
			defer f.Close()

			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				// Catch all forms: `"net/http"`, `http "net/http"`, `_ "net/http"`
				if strings.Contains(line, `"net/http"`) {
					t.Errorf("%s imports net/http directly -- use fetchRenderedHTML instead; line: %s", filename, strings.TrimSpace(line))
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan error in %s: %v", filename, err)
			}
			t.Logf("FF_NoDirectNetHTTP PASS: %s does not import net/http", filename)
		})
	}
}
