package typst

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ErrBinaryNotFound is returned (wrapped) when the typst or pandoc binary is
// not on PATH and no RENDER_*_PATH env var is set. Callers can distinguish
// "deployment misconfiguration" (soft skip) from a real compile failure via
// errors.Is(err, ErrBinaryNotFound).
var ErrBinaryNotFound = errors.New("typst: required binary not found")

// ErrorDetails contains the parsed details of a single typst diagnostic.
type ErrorDetails struct {
	Message string // The error message, e.g. "file not found (searched at …)".
	Path    string // Source path as reported by typst; "<stdin>" for stdio input. Empty when absent.
	Line    int    // 1-based line number; 0 when absent.
	Column  int    // 1-based column number; 0 when absent.
}

// CompileError is a structured error returned by compileTypst when the typst
// CLI exits non-zero. The raw stderr is preserved in Raw; Details holds the
// parsed diagnostics (one per error block). Callers use errors.As to access
// Details and surface line/column to end users.
type CompileError struct {
	Inner   error  // the underlying exec.ExitError or context error
	Raw     string // verbatim stderr
	Details []ErrorDetails
}

func (e *CompileError) Error() string { return e.Raw }
func (e *CompileError) Unwrap() error { return e.Inner }

// stderrRegex parses a single typst diagnostic block in --diagnostic-format human.
// typst 0.14 emits:
//
//	error: <message>
//	  ┌─ <path>:<line>:<column>
//
// The message may span multiple lines (hence (?s) and lazy .+?), e.g. warnings
// carry "= hint:" continuation lines. The path line is optional — some
// diagnostics (e.g. CLI argument errors) omit it. The error group is anchored
// to "error:", "warning:", or "help:" so non-diagnostic noise (e.g. a bare
// panic trace without the prefix) is not mis-parsed as a CompileError. The
// "help:" prefix covers stacked diagnostics where typst reports the import
// source that triggered an error in a separate block.
var stderrRegex = regexp.MustCompile(`(?s)^(?P<error>(?:error|warning|help): .*?)` +
	`(?:(?:\n\s+┌─ (?P<path>.+?):(?P<line>\d+):(?P<column>\d+)\n)|(?:$))`)

// ParseStderr parses typst stderr (diagnostic-format human) into a CompileError.
// Returns nil (not an error) when stderr contains no parseable diagnostic
// blocks — the caller should then wrap the raw stderr itself.
func ParseStderr(stderr string, inner error) *CompileError {
	ce := &CompileError{Inner: inner, Raw: stderr}

	// Typst separates diagnostic blocks with a blank line. The trailing split
	// element (after the final \n\n) is empty or non-diagnostic noise; drop it.
	parts := strings.Split(stderr, "\n\n")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}

	for _, part := range parts {
		parsed := stderrRegex.FindStringSubmatch(part)
		if parsed == nil {
			continue
		}
		var d ErrorDetails
		if i := stderrRegex.SubexpIndex("error"); i > 0 && i < len(parsed) {
			d.Message = strings.TrimSpace(parsed[i])
		}
		if i := stderrRegex.SubexpIndex("path"); i > 0 && i < len(parsed) {
			d.Path = parsed[i]
		}
		if i := stderrRegex.SubexpIndex("line"); i > 0 && i < len(parsed) && parsed[i] != "" {
			if line, err := strconv.ParseInt(parsed[i], 10, 0); err == nil {
				d.Line = int(line)
			}
		}
		if i := stderrRegex.SubexpIndex("column"); i > 0 && i < len(parsed) && parsed[i] != "" {
			if col, err := strconv.ParseInt(parsed[i], 10, 0); err == nil {
				d.Column = int(col)
			}
		}
		ce.Details = append(ce.Details, d)
	}

	if len(ce.Details) == 0 {
		return nil
	}
	return ce
}
