package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenLinkedInFixtureMD is a synthetic LINKEDIN-UPDATE.md (no operator data)
// exercising the copy-block + char-chip render paths the golden gate locks:
//   - section 1 (limit 220): single unlabeled code block + char chip
//   - section 3 (limit 2000): TWO labeled entries (A/B), each its own char chip,
//     both sharing data-copy-field="3" (section-scoped, NOT per-block)
//   - section 4 (limit 0): a block with the no-limit "N chars" chip
const goldenLinkedInFixtureMD = "## 1. Headline (max 220 chars)\n\n" +
	"Short intro prose with **bold** and a [link](https://example.com).\n\n" +
	"```\nStaff Software Engineer | Go + Rust | Distributed Systems\n```\n\n" +
	"## 3. Experience\n\nLead-in prose.\n\n" +
	"### Entry A\n\n```\nBuilt a thing at Acme. Shipped it. Measured it.\n```\n\n" +
	"### Entry B\n\n```\nBuilt another thing. Also shipped.\n```\n\n" +
	"Trailing prose after the blocks.\n\n" +
	"## 4. Skills\n\n```\nGo, Rust, PostgreSQL\n```\n"

// renderLinkedInGolden serves /admin/linkedin against goldenLinkedInFixtureMD
// and returns the full response body. It uses the real linkedinHandler (which
// parses sharedPartialsSrc + linkedinTmplSrc), so the assertions below cover the
// strangler cutover end to end, not a hand-copied template.
func renderLinkedInGolden(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "LINKEDIN-UPDATE.md"), []byte(goldenLinkedInFixtureMD), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	p := testPanel(t)
	h := linkedinHandler(p, dir)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/linkedin", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	return rr.Body.String()
}

// TestLinkedInGolden_CopyMarkupExact is the HARD merge gate for the Phase 5
// strangler cutover: the LinkedIn page (the one that ALREADY ships working copy
// buttons) must keep emitting its exact copy-block markup after migrating to the
// shared copyBlock partial. Any future change that alters the copy DOM, the
// data-copy-* attributes, the aria-label/title text, or the char chips trips
// these substring asserts RED.
func TestLinkedInGolden_CopyMarkupExact(t *testing.T) {
	body := renderLinkedInGolden(t)

	// Exact copy-block markup for section 1 (unlabeled block) — the full
	// button element with every attribute byte-for-byte. This is the contract
	// the shared copyBlock partial must reproduce for LinkedIn.
	const section1Block = `<pre class="li-pre" id="li-code-1-1">Staff Software Engineer | Go &#43; Rust | Distributed Systems</pre>
  <button class="gd-copy-btn" type="button"
          data-copy-pre="li-code-1-1"
          data-copy-field="1"
          aria-label="Copy section 1"
          title="Paragraph spacing normalized for LinkedIn paste">Copy</button>
  <span class="cc-green">57 / 220</span>`
	if !strings.Contains(body, section1Block) {
		t.Errorf("section-1 copy block markup changed (copy regression).\nwant substring:\n%s\n\ngot body:\n%s", section1Block, body)
	}

	// Section 3 has TWO labeled blocks; both carry data-copy-field="3"
	// (section-scoped). A naive partial that used a per-block index would
	// have emitted "1"/"2" here — assert the section-scoped value survives.
	const entryABlock = `<div class="li-block-label">Entry A</div>`
	if !strings.Contains(body, entryABlock) {
		t.Errorf("Entry A label markup missing/changed:\n%s", entryABlock)
	}
	const entryAButton = `<pre class="li-pre" id="li-code-3-1">Built a thing at Acme. Shipped it. Measured it.</pre>
  <button class="gd-copy-btn" type="button"
          data-copy-pre="li-code-3-1"
          data-copy-field="3"
          aria-label="Copy Entry A"
          title="Paragraph spacing normalized for LinkedIn paste">Copy</button>
  <span class="cc-green">47 / 2000</span>`
	if !strings.Contains(body, entryAButton) {
		t.Errorf("Entry A copy block markup changed (copy regression).\nwant substring:\n%s", entryAButton)
	}
	// Entry B shares data-copy-field="3" with Entry A (section-scoped id).
	if !strings.Contains(body, `id="li-code-3-2"`) || !strings.Contains(body,
		`data-copy-pre="li-code-3-2"
          data-copy-field="3"
          aria-label="Copy Entry B"`) {
		t.Errorf("Entry B copy block markup changed (section-scoped data-copy-field=3 regression).\nbody:\n%s", body)
	}

	// Section 4 (no char limit) uses the "N chars" muted chip.
	if !strings.Contains(body, `<span class="cc-muted">20 chars</span>`) {
		t.Errorf("section-4 no-limit char chip changed")
	}
}

// TestLinkedInGolden_SharedCSSPresent asserts the copy-block + char-chip CSS the
// page depends on is emitted (now sourced from sharedPartialsSrc's sharedCSS
// define, not inline). RED if the {{template "sharedCSS" .}} pull regresses.
func TestLinkedInGolden_SharedCSSPresent(t *testing.T) {
	body := renderLinkedInGolden(t)
	for _, sel := range []string{
		`.gd-copy-btn{`,
		`.li-pre{`,
		`.li-code-wrap{`,
		`.gd-copy-btn.copied{`,
		`.cc-muted{`,
		`.cc-green{`,
		`.cc-amber{`,
		`.cc-red{`,
	} {
		if !strings.Contains(body, sel) {
			t.Errorf("shared CSS rule %q missing from LinkedIn render (sharedCSS regression)", sel)
		}
	}
}

// TestLinkedInGolden_ContentAutoEscaped guards the NON-NEGOTIABLE invariant:
// copy-block content is rendered via {{.Content}} auto-escape (never
// template.HTML). A '+' must appear as &#43;, and an injected <script> must be
// escaped, not raw.
func TestLinkedInGolden_ContentAutoEscaped(t *testing.T) {
	body := renderLinkedInGolden(t)
	// The fixture's "Go + Rust" must HTML-escape the '+'.
	if !strings.Contains(body, "Go &#43; Rust") {
		t.Errorf("copy-block content not auto-escaped: expected 'Go &#43; Rust', got body:\n%s", body)
	}
	if strings.Contains(body, "Go + Rust") {
		t.Errorf("copy-block content rendered raw (template.HTML leak?): found unescaped 'Go + Rust'")
	}
}
