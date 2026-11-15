package applications

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-kit/uploads"
	"github.com/google/uuid"
)

// JobMarker records, in the corpus DB, that an account generated application
// artifacts for a job. It is the consumer-side port that keeps this package
// DB-free: main wires it to hunt.Store.PinJob. Retention (hunt.PruneStale)
// keeps every pinned job (hunt_retention_pins), so pinning is what stops the
// prune from orphaning the files keyed by job id.
type JobMarker func(ctx context.Context, aid uuid.UUID, jobID int64) error

// ErrNoJobMarker is returned by Persist when RequireJobMarker was called but no
// marker is installed: writing artifacts the prune cannot see would orphan them.
var ErrNoJobMarker = errors.New("applications: job marker required (retention enabled) but not installed")

// SetJobMarker installs the marker Persist calls before writing any file. Call
// once at boot, before the authority serves requests (not synchronised).
func (a *Authority) SetJobMarker(m JobMarker) { a.marker = m }

// RequireJobMarker makes Persist fail with ErrNoJobMarker while no marker is
// installed. main calls it whenever retention is enabled, so a forgotten
// SetJobMarker is a loud error instead of silently unmarked artifacts.
func (a *Authority) RequireJobMarker() { a.requireMarker = true }

// ArtifactJobRefs lists, by directory name only (no file is opened), every job
// that has an artifact directory: the account-scoped
// applications/<account_uuid>/<job_id>/ layout, plus the pre-P4 account-less
// applications/<job_id>/ layout, attributed to the operator account (the only
// account that may resolve it). The two slices are parallel. A missing uploads
// base means no artifacts; any other read error is returned (an unreadable
// store must not look empty).
func (a *Authority) ArtifactJobRefs() (accountIDs []uuid.UUID, jobIDs []int64, err error) {
	base := filepath.Join(uploads.Root(), "go-job", "applications")
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("applications: read artifact base: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if id, perr := strconv.ParseInt(e.Name(), 10, 64); perr == nil && id > 0 {
			if a.operator != uuid.Nil {
				accountIDs, jobIDs = append(accountIDs, a.operator), append(jobIDs, id)
			}
			continue
		}
		aid, perr := uuid.Parse(e.Name())
		if perr != nil || aid == uuid.Nil {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(base, e.Name()))
		if err != nil {
			return nil, nil, fmt.Errorf("applications: read artifact dir of account %s: %w", aid, err)
		}
		for _, s := range subs {
			if id, perr := strconv.ParseInt(s.Name(), 10, 64); perr == nil && id > 0 && s.IsDir() {
				accountIDs, jobIDs = append(accountIDs, aid), append(jobIDs, id)
			}
		}
	}
	return accountIDs, jobIDs, nil
}

// LegacyOwner is the account the legacy artifact locations belong to
// (uuid.Nil: none resolve).
func (a *Authority) LegacyOwner() uuid.UUID { return a.operator }

// LegacyMatcher returns a company-level matcher over the legacy
// APPLICATIONS_DIR slug directories: a company matches when slugify(company)
// equals a directory name or is its leading token run ("anthropic" matches
// "anthropic-databases"). This is a strict superset of the per-job fuzzy match
// LegacyResolve performs, and it does not depend on a job's current title or
// age. dirs is the number of slug directories read.
//
// Fails closed: legacy disabled (no operator or no APPLICATIONS_DIR) returns
// (nil, 0, nil); a configured directory that cannot be read, or that has no
// directories, is an error, because a lost mount would otherwise look like
// "nothing to protect".
func (a *Authority) LegacyMatcher() (match func(company string) bool, dirs int, err error) {
	if a.operator == uuid.Nil || a.legacyDir == "" {
		return nil, 0, nil
	}
	entries, err := os.ReadDir(a.legacyDir)
	if err != nil {
		return nil, 0, fmt.Errorf("applications: read legacy dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, 0, fmt.Errorf("applications: legacy dir %q is configured but has no directories", a.legacyDir)
	}
	return func(company string) bool {
		cs := slugify(company)
		if cs == "" {
			return false
		}
		for _, n := range names {
			if n == cs || strings.HasPrefix(n, cs+"-") {
				return true
			}
		}
		return false
	}, len(names), nil
}
