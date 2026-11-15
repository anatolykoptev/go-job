package applications

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anatolykoptev/go-kit/uploads"
	"github.com/google/uuid"
)

// JobMarker records, in the corpus DB, that an account generated application
// artifacts for a job. It is the consumer-side port that keeps this package
// DB-free: main wires it to hunt.AccountStore.MarkArtifactJob. Retention
// (hunt.PruneStale) keeps every job that has a hunt_ratings row, so marking
// is what stops the prune from orphaning the files keyed by job id.
type JobMarker func(ctx context.Context, aid uuid.UUID, jobID int64) error

// SetJobMarker installs the marker Persist calls before writing any file. Call
// once at boot, before the authority serves requests (not synchronised).
func (a *Authority) SetJobMarker(m JobMarker) { a.marker = m }

// ArtifactJobRefs lists, by directory name only (no file is opened), every job
// that has an artifact directory: the account-scoped
// applications/<account_uuid>/<job_id>/ layout, plus the pre-P4 account-less
// applications/<job_id>/ layout, attributed to the operator account (the only
// account that may resolve it). The two slices are parallel.
func (a *Authority) ArtifactJobRefs() (accountIDs []uuid.UUID, jobIDs []int64) {
	base := filepath.Join(uploads.Root(), "go-job", "applications")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && id > 0 {
			if a.operator != uuid.Nil {
				accountIDs, jobIDs = append(accountIDs, a.operator), append(jobIDs, id)
			}
			continue
		}
		aid, err := uuid.Parse(e.Name())
		if err != nil || aid == uuid.Nil {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(base, e.Name()))
		if err != nil {
			continue
		}
		for _, s := range subs {
			if id, err := strconv.ParseInt(s.Name(), 10, 64); err == nil && id > 0 && s.IsDir() {
				accountIDs, jobIDs = append(accountIDs, aid), append(jobIDs, id)
			}
		}
	}
	return accountIDs, jobIDs
}

// LegacyOwner is the account the legacy artifact locations belong to
// (uuid.Nil: none resolve).
func (a *Authority) LegacyOwner() uuid.UUID { return a.operator }

// LegacyMatcher returns a function reporting whether the legacy APPLICATIONS_DIR
// holds a resume or cover PDF for a job's company and title (the same fuzzy
// slug match LegacyResolve uses). The directory is read once. Nil when legacy
// lookups are disabled.
func (a *Authority) LegacyMatcher() func(company, title string) bool {
	if a.operator == uuid.Nil || a.legacyDir == "" {
		return nil
	}
	ac := a.ForAccount(a.operator)
	entries := ac.LegacyEntries()
	if len(entries) == 0 {
		return nil
	}
	return func(company, title string) bool {
		return ac.LegacyExistsFromEntries(entries, company, title, KindResume) ||
			ac.LegacyExistsFromEntries(entries, company, title, KindCover)
	}
}
