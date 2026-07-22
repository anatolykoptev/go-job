package jobs

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestResumeMigrationsIdempotent guards the resumedb apply-all invariant.
//
// resumedb runs pgutil.RunMigrations with NO Baseline: on every boot the
// runner applies every .sql file in lexical order that is not already
// recorded in resume_schema_migrations. On the existing prod DB the first
// pgutil run therefore re-executes every non-soft file. This is safe ONLY
// because every non-soft file is idempotent (CREATE TABLE IF NOT EXISTS /
// ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS, no
// DROP/DELETE/UPDATE/INSERT). A future file that violates this would
// silently corrupt the live DB on boot — re-running a non-idempotent
// statement against production data is destructive and (for DROP/DELETE)
// irreversible.
//
// pgutil skips files already recorded in resume_schema_migrations, so after
// the first pgutil run the apply-all path is never re-entered. But the FIRST
// run on the existing prod DB (the cutover boot) DOES re-execute every
// non-soft file, and any future migration added later is also re-executed
// on the next boot if it was not yet recorded. The guard must hold for
// every non-soft file, always.
//
// This is a pure file-content test — no DB needed, runs in every
// environment (never t.Skip). Soft files (first line "-- soft") are exempt:
// they run on the apply-all path only when the optional extension is
// present, and a soft failure is tolerated by pgutil (warn + skip, not
// abort). The non-soft files MUST be safe to re-run unconditionally.
func TestResumeMigrationsIdempotent(t *testing.T) {
	// Same embed FS runMigrations uses — the test guards the exact corpus
	// the runner applies, so it cannot drift from production.
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		t.Fatalf("read embedded schema dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("resume schema embed dir contains no .sql files")
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data, rerr := fs.ReadFile(schemaFS, "schema/"+name)
			if rerr != nil {
				t.Fatalf("read %s: %v", name, rerr)
			}
			body := string(data)

			// Soft files are exempt — they only run when the optional
			// extension (AGE / pgvector) is present, and a soft failure is
			// tolerated (warn + skip, not abort). The apply-all path's
			// safety guarantee only covers non-soft files.
			if isSoftMigration(body) {
				t.Logf("%s is soft (first line -- soft); exempt from idempotency guard", name)
				return
			}

			// Strip line comments before pattern matching so a comment like
			// `-- 'resume', 'inferred', 'enrichment'` or a commented-out
			// `-- DROP TABLE foo` does not trip the guard. We do NOT strip
			// block comments (none appear in the resume schema); a
			// statement-spanning block comment would be a real risk worth
			// catching.
			code := stripLineComments(body)

			for _, bad := range nonIdempotentPatterns {
				if loc := bad.find(code); loc != nil {
					t.Errorf("%s: non-idempotent statement %q found — %s\n"+
						"resumedb runs pgutil with NO Baseline, so every non-soft file is re-executed on the cutover boot and on any boot where it is not yet recorded in resume_schema_migrations. Re-running this statement against a populated DB would corrupt live data. Make the statement idempotent (add IF NOT EXISTS / guard with a DO $$ ... IF NOT EXISTS block) or mark the file -- soft if it requires a one-shot side effect.\n"+
						"offending text: %q",
						name, bad.label, bad.reason, snippet(code, loc[0], loc[1]))
				}
			}
		})
	}
}

// isSoftMigration reports whether the file's first non-whitespace line is
// "-- soft" (pgutil's soft-marker, matched case-sensitively per pgutil).
func isSoftMigration(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return trimmed == "-- soft"
	}
	return false
}

// stripLineComments removes `-- ...` trailing and full-line comments from
// each line, preserving the newline so line-based regex offsets stay
// meaningful. A statement keyword that appears inside a `--` comment is
// not a real occurrence.
func stripLineComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// snippet returns a short window around the match for the failure message.
func snippet(s string, start, end int) string {
	const window = 40
	lo := start - window
	if lo < 0 {
		lo = 0
	}
	hi := end + window
	if hi > len(s) {
		hi = len(s)
	}
	return strings.Join(strings.Fields(s[lo:hi]), " ")
}

// nonIdempotentPattern describes a statement shape that would corrupt a
// populated DB when re-executed by the apply-all path.
//
// Allowed (idempotent) shapes that the matcher deliberately does NOT flag:
//   - `CREATE TABLE IF NOT EXISTS`
//   - `ADD COLUMN IF NOT EXISTS`
//   - `CREATE INDEX IF NOT EXISTS` and `CREATE INDEX CONCURRENTLY IF NOT EXISTS`
//
// Go's regexp is RE2 and has no lookahead, so the "without IF NOT EXISTS"
// checks are implemented as: find the leader token (e.g. `CREATE TABLE`),
// then inspect the following non-whitespace token in Go — if it is `IF`
// the statement is the guarded shape and is allowed, otherwise it is a
// finding.
type nonIdempotentPattern struct {
	label  string
	reason string
	// leader matches the leading keyword sequence (case-insensitive).
	leader *regexp.Regexp
	// allowIf reports whether the statement is the guarded (idempotent)
	// shape when the token immediately following the leader is "IF". For
	// CREATE INDEX, "CONCURRENTLY" may precede "IF", so the matcher skips
	// an optional CONCURRENTLY token before the IF check.
	allowConcurrently bool
	// excludeOnPrefix suppresses a match when the token immediately
	// preceding the leader is "ON". This excludes FK referential actions
	// (`ON DELETE CASCADE`, `ON UPDATE SET NULL`) which are part of a
	// REFERENCES clause inside CREATE TABLE — not standalone DML, and
	// idempotent via the enclosing CREATE TABLE IF NOT EXISTS.
	excludeOnPrefix bool
}

func (p nonIdempotentPattern) find(code string) []int {
	locs := p.leader.FindAllStringIndex(code, -1)
	for _, loc := range locs {
		// Exclude FK referential actions: ON DELETE / ON UPDATE.
		if p.excludeOnPrefix && precededByToken(code, loc[0], "ON") {
			continue
		}
		rest := code[loc[1]:]
		// Skip whitespace.
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
			i++
		}
		rest = rest[i:]
		// Optional CONCURRENTLY for CREATE INDEX.
		if p.allowConcurrently {
			if tok := nextToken(rest); strings.EqualFold(tok, "CONCURRENTLY") {
				rest = rest[len("CONCURRENTLY"):]
				// skip whitespace again
				j := 0
				for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\n' || rest[j] == '\r') {
					j++
				}
				rest = rest[j:]
			}
		}
		if tok := nextToken(rest); strings.EqualFold(tok, "IF") {
			// Guarded shape (IF NOT EXISTS follows) — allowed.
			continue
		}
		// Finding: return the leader's location so the failure message can
		// quote the offending text.
		return loc
	}
	return nil
}

// precededByToken reports whether the token immediately preceding position
// `pos` in `code` (skipping whitespace) equals `want` case-insensitively.
func precededByToken(code string, pos int, want string) bool {
	i := pos
	for i > 0 {
		c := code[i-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i--
			continue
		}
		break
	}
	// Walk back over identifier characters.
	j := i
	for j > 0 {
		c := code[j-1]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '"' {
			j--
			continue
		}
		break
	}
	if j == i {
		return false
	}
	return strings.EqualFold(code[j:i], want)
}

// nextToken returns the leading run of [A-Za-z0-9_".] characters of s
// (an unquoted SQL identifier or the keyword `IF`), uppercased for
// comparison. Returns "" if s starts with non-identifier characters.
func nextToken(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '"' {
			i++
			continue
		}
		break
	}
	return s[:i]
}

var nonIdempotentPatterns = []nonIdempotentPattern{
	{
		label:             "CREATE TABLE without IF NOT EXISTS",
		reason:            "re-executing CREATE TABLE against an existing table errors (the table already holds live data); use CREATE TABLE IF NOT EXISTS",
		leader:            regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+`),
		allowConcurrently: false,
	},
	{
		label:             "ADD COLUMN without IF NOT EXISTS",
		reason:            "re-executing ADD COLUMN against an existing column errors; use ADD COLUMN IF NOT EXISTS",
		leader:            regexp.MustCompile(`(?i)\bADD\s+COLUMN\s+`),
		allowConcurrently: false,
	},
	{
		label:             "CREATE INDEX without IF NOT EXISTS",
		reason:            "re-executing CREATE INDEX against an existing index errors; use CREATE INDEX IF NOT EXISTS (or CREATE INDEX CONCURRENTLY IF NOT EXISTS)",
		leader:            regexp.MustCompile(`(?i)\bCREATE\s+INDEX\s+`),
		allowConcurrently: true,
	},
	{
		label:  "DROP statement",
		reason: "DROP is destructive and not idempotent (re-running against already-dropped or in-use objects errors or destroys live data)",
		// DROP has no IF-guarded idempotent shape in the resume schema; the
		// token following `DROP ` is the object kind (TABLE/INDEX/...),
		// never `IF`, so find() always returns it as a finding.
		leader: regexp.MustCompile(`(?i)\bDROP\s+`),
	},
	{
		label:           "DELETE statement",
		reason:          "DELETE mutates live data and is not idempotent (a second run deletes a different/empty row set or errors)",
		leader:          regexp.MustCompile(`(?i)\bDELETE\s+`),
		excludeOnPrefix: true, // skip `ON DELETE CASCADE/SET ...` FK referential actions
	},
	{
		label:           "UPDATE statement",
		reason:          "UPDATE mutates live data and is not idempotent (a second run re-applies the change to already-updated rows or different rows)",
		leader:          regexp.MustCompile(`(?i)\bUPDATE\s+`),
		excludeOnPrefix: true, // skip `ON UPDATE CASCADE/SET ...` FK referential actions
	},
	{
		label:  "INSERT statement",
		reason: "INSERT is not idempotent (a second run duplicates rows or errors on a UNIQUE constraint); use INSERT ... ON CONFLICT DO NOTHING for seed/idempotent inserts",
		// `INSERT INTO ...` — the token after INSERT is INTO, never IF.
		leader: regexp.MustCompile(`(?i)\bINSERT\s+`),
	},
}
