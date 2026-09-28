package adminui

// account_isolation_test.go — the adminui half of the P2 deny matrix: the
// jobs lister and job detailer render the ACTING account's
// account_job_scores row and nothing else. Runs on real PostgreSQL; the
// accountResolver stand-ins (fixedAccount/denyAccount) emulate the session /
// pin seams at the data plane.
//
// RED-on-revert: dropping `AND s.account_id = $N` from jobsLister's or
// jobDetailQuery's score join returns A's score to B — the NULL-score
// assertions below fail.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobsLister_AccountIsolation(t *testing.T) {
	pool := openJobsPool(t)
	ctx := context.Background()

	store := hunt.NewStore(pool)
	require.NoError(t, store.Migrate(ctx))
	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)

	// One shared job; score it under A only.
	h := hunt.DedupHash("https://iso.example/jobs/lister")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, status)
		VALUES ($1, 'Iso Lister Role', 'IsoCorp', 'https://iso.example/jobs/lister', 'iso_test', 'open')
		ON CONFLICT (dedup_hash) DO UPDATE SET status='open'
		RETURNING id`, h).Scan(&jobID))
	_, err := pool.Exec(ctx, `
		INSERT INTO account_job_scores
			(account_id, job_id, fit_score, fit_band, success_band, over_under, scored_at)
		VALUES ($1, $2, 91, 'strong', 'STRONG', 'well_matched', $3)
		ON CONFLICT (account_id, job_id) DO UPDATE SET fit_score = 91`,
		aidA, jobID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_job_scores WHERE job_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	q := resource.ListQuery{
		Sort:       jobsSpec.Resolve("fit", "desc"),
		WhereConds: "j.id = $1",
		WhereArgs:  []any{jobID},
		Limit:      25,
	}

	rowsA, _, err := jobsLister(pool, nil, nil, fixedAccount(aidA))(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsA, 1)
	assert.Contains(t, rowsA[0].Cells[5].Value, ">91", "A's lister renders A's score")

	rowsB, _, err := jobsLister(pool, nil, nil, fixedAccount(aidB))(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsB, 1)
	assert.NotContains(t, rowsB[0].Cells[5].Value, ">91", "B's lister must never render A's score")
	assert.Contains(t, rowsB[0].Cells[5].Value, "fit-unscored", "B renders the unscored chip")

	// Denied resolver → uuid.Nil binds → unscored, never A's row.
	rowsNil, _, err := jobsLister(pool, nil, nil, denyAccount())(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsNil, 1)
	assert.NotContains(t, rowsNil[0].Cells[5].Value, ">91")
}

// TestJobDetail_AccountIsolation runs scanJobDetail — the query behind both
// the detailer and the rescore handler — under A, B and the nil account.
func TestJobDetail_AccountIsolation(t *testing.T) {
	pool := openJobsPool(t)
	ctx := context.Background()

	store := hunt.NewStore(pool)
	require.NoError(t, store.Migrate(ctx))
	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)

	h := hunt.DedupHash("https://iso.example/jobs/detail")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, status)
		VALUES ($1, 'Iso Detail Role', 'IsoCorp', 'https://iso.example/jobs/detail', 'iso_test', 'open')
		ON CONFLICT (dedup_hash) DO UPDATE SET status='open'
		RETURNING id`, h).Scan(&jobID))
	_, err := pool.Exec(ctx, `
		INSERT INTO account_job_scores
			(account_id, job_id, fit_score, fit_band, success_band, over_under, score_rationale, scored_at)
		VALUES ($1, $2, 88, 'strong', 'STRONG', 'well_matched', '{"fit_reasons":["a-only"],"fit_gaps":[],"success_reasoning":"a"}'::jsonb, $3)`,
		aidA, jobID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_job_scores WHERE job_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	recA, err := scanJobDetail(ctx, pool, jobID, aidA)
	require.NoError(t, err)
	require.NotNil(t, recA.FitScore)
	assert.Equal(t, 88, *recA.FitScore)
	assert.Equal(t, "strong", recA.FitBand)
	assert.Contains(t, string(recA.RationaleRaw), "a-only")

	recB, err := scanJobDetail(ctx, pool, jobID, aidB)
	require.NoError(t, err)
	assert.Nil(t, recB.FitScore, "B's detail must not show A's score")
	assert.Empty(t, recB.FitBand)
	assert.Empty(t, recB.RationaleRaw)

	recNil, err := scanJobDetail(ctx, pool, jobID, uuid.Nil)
	require.NoError(t, err)
	assert.Nil(t, recNil.FitScore, "nil account renders unscored, never a leak")
}

// TestShortlistLister_AccountIsolation exercises the resource-facing wrapper
// (shortlistLister → ForAccount → ListShortlist) — not just the store — so a
// wiring regression in the acctOf→facade hand-off is caught at the handler
// layer. Fit chip is Cells[4].
func TestShortlistLister_AccountIsolation(t *testing.T) {
	pool := openJobsPool(t)
	ctx := context.Background()

	store := hunt.NewStore(pool)
	require.NoError(t, store.Migrate(ctx))
	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)

	h := hunt.DedupHash("https://iso.example/jobs/shortlist")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, status)
		VALUES ($1, 'Iso Shortlist Role', 'IsoCorp', 'https://iso.example/jobs/shortlist', 'iso_test_sl', 'open')
		ON CONFLICT (dedup_hash) DO UPDATE SET status='open'
		RETURNING id`, h).Scan(&jobID))
	_, err := pool.Exec(ctx, `
		INSERT INTO account_job_scores
			(account_id, job_id, fit_score, fit_band, success_band, over_under, scored_at)
		VALUES ($1, $2, 91, 'strong', 'STRONG', 'well_matched', $3)`,
		aidA, jobID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_job_scores WHERE job_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_ratings WHERE entry_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	require.NoError(t, store.ForAccount(aidA).Rate(ctx, "job", jobID, hunt.StageSaved, "", ""))

	authority := applications.New(nil, t.TempDir())
	listerA := shortlistLister(store, authority, nil, fixedAccount(aidA))
	listerB := shortlistLister(store, authority, nil, fixedAccount(aidB))

	q := resource.ListQuery{Sort: shortlistSpec.Resolve("fit", "desc"), Limit: 25}

	rowsA, _, err := listerA(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsA, 1)
	assert.Contains(t, rowsA[0].Cells[4].Value, ">91", "A's shortlist renders A's score")

	// Ratings are account-owned (ADR-15): A's 'saved' is A's judgment — B's
	// shortlist is empty until B rates the job itself.
	rowsB, _, err := listerB(ctx, q)
	require.NoError(t, err)
	require.Empty(t, rowsB, "B must never see A's rating row")

	require.NoError(t, store.ForAccount(aidB).Rate(ctx, "job", jobID, hunt.StageSaved, "", ""))
	rowsB, _, err = listerB(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsB, 1, "B sees its own rating row after rating")
	assert.Contains(t, rowsB[0].Cells[4].Value, "fit-unscored",
		"B's shortlist chip is unscored — A's score unreachable")
}

// TestRescoreHandler_NoAccount_403 pins the write-side deny: with no
// resolvable account the handler refuses before touching the DB — a score
// with no owning account has nowhere to land.
func TestRescoreHandler_NoAccount_403(t *testing.T) {
	handler := rescoreHandler(nil, nil, denyAccount())
	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/jobs/123/rescore", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "123")

	rr := httptest.NewRecorder()
	handler(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code,
		"rescore without a resolvable account must deny (no DB write possible)")
}

// TestRatings_AccountIsolation pins ratings isolation on the read paths the
// detailer and tracker drive (P3 deny matrix, ADR-15): A's rating on a shared
// corpus job is invisible to B — GetRating is ErrNotFound, ListRatings and
// the tracked list are empty — until B writes its own row, which never
// disturbs A's. RED-on-revert: restoring user_name scoping (or dropping the
// account predicate) returns A's row to B.
func TestRatings_AccountIsolation(t *testing.T) {
	pool := openJobsPool(t)
	ctx := context.Background()

	store := hunt.NewStore(pool)
	require.NoError(t, store.Migrate(ctx))
	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)

	// One shared job in the global corpus; only A rates it (both axes + note).
	h := hunt.DedupHash("https://iso.example/jobs/ratings")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, status)
		VALUES ($1, 'Iso Ratings Role', 'IsoCorp', 'https://iso.example/jobs/ratings', 'iso_test', 'open')
		ON CONFLICT (dedup_hash) DO UPDATE SET status='open'
		RETURNING id`, h).Scan(&jobID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_ratings WHERE entry_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	require.NoError(t, store.ForAccount(aidA).Rate(ctx, "job", jobID,
		hunt.StageInteresting, "applied", "a-only note"))

	// B's view of the same job: unrated on every read path — the detailer's
	// GetRating, the ratings list, and the tracker list all see no row FOR B.
	_, err := store.ForAccount(aidB).GetRating(ctx, "job", jobID)
	assert.ErrorIs(t, err, hunt.ErrNotFound, "B must not read A's rating")
	ratsB, err := store.ForAccount(aidB).ListRatings(ctx, hunt.RatingFilter{Kind: hunt.KindJob})
	require.NoError(t, err)
	assert.Empty(t, ratsB, "B's rating list must not contain A's row")
	trackedB, totalB, err := store.ForAccount(aidB).ListTrackedJobs(ctx, hunt.TrackedFilter{})
	require.NoError(t, err)
	assert.Zero(t, totalB)
	assert.Empty(t, trackedB, "B's tracker must not surface a job only A rated")

	// uuid.Nil fails closed — never a cross-account read.
	_, err = store.ForAccount(uuid.Nil).GetRating(ctx, "job", jobID)
	assert.ErrorIs(t, err, hunt.ErrNotFound, "nil account never reads a rating")

	// B rates the same job differently — both rows coexist; A's is untouched.
	require.NoError(t, store.ForAccount(aidB).Rate(ctx, "job", jobID,
		hunt.StageDiscarded, "", "b-only note"))

	ratA, err := store.ForAccount(aidA).GetRating(ctx, "job", jobID)
	require.NoError(t, err)
	assert.Equal(t, hunt.StageInteresting, ratA.Triage)
	assert.Equal(t, "applied", ratA.Stage)
	assert.Equal(t, "a-only note", ratA.Note, "B's write must not touch A's row")

	ratB, err := store.ForAccount(aidB).GetRating(ctx, "job", jobID)
	require.NoError(t, err)
	assert.Equal(t, hunt.StageDiscarded, ratB.Triage)
	assert.Equal(t, "b-only note", ratB.Note)

	// Exactly two rows on the shared job, one per account.
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM hunt_ratings WHERE entry_kind = 'job' AND entry_id = $1`, jobID).Scan(&n))
	assert.Equal(t, 2, n, "one ratings row per account on a shared job")
}

// TestHuntSettingsResource_AccountIsolation pins the P3 settings boundary
// (ADR-7): the resource's row key is the ACTING account — a missing
// account_hunt_settings row renders disabled/fail-closed defaults (never
// another account's row), a write lands only under the resolved account, and
// a request with no verified account is denied at every entry point.
// RED-on-revert: keying the resource by a shared row (or ignoring acctOf)
// leaks A's enabled row into B's FetchRow/Load.
func TestHuntSettingsResource_AccountIsolation(t *testing.T) {
	pool := openJobsPool(t)
	ctx := context.Background()

	store := hunt.NewStore(pool)
	require.NoError(t, store.Migrate(ctx))
	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)

	resA := huntSettingsResource(store, fixedAccount(aidA))
	resB := huntSettingsResource(store, fixedAccount(aidB))
	resDeny := huntSettingsResource(store, denyAccount())

	// Missing row → disabled/fail-closed defaults, never another row's values.
	rowB, err := resB.FetchRow(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "", rowB["enabled"], "missing row renders disabled")
	assert.Equal(t, "", rowB["score_enabled"], "missing row renders scoring off")
	assert.Equal(t, "", rowB["score_fail_open"], "missing row renders fail-closed")

	// The lister yields one account-keyed row — the SingleRow redirect target —
	// reflecting this account's own (default) state.
	rows, total, err := resB.Lister(ctx, resource.ListQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, aidB.String(), rows[0].ID, "list row keys on the acting account")

	// A saves a fully armed row through the resource.
	require.NoError(t, resA.Writer.Save(ctx, tenant.Tenant{}, "", map[string]string{
		"enabled":           "on",
		"queries":           "a queries",
		"notify_chat_id":    "111",
		"notify_min_fit":    "70",
		"notify_max_age":    "24h",
		"score_enabled":     "on",
		"score_fail_open":   "on",
		"score_min_jaccard": "20",
	}))

	// B still reads its own absent row — A's armed row is unreachable.
	rowB, err = resB.FetchRow(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "", rowB["enabled"], "B must never read A's enabled row")
	loadB, err := resB.Writer.Load(ctx, tenant.Tenant{}, "")
	require.NoError(t, err)
	assert.Equal(t, "", loadB["enabled"])

	// B writes its own row — A's row is untouched.
	require.NoError(t, resB.Writer.Save(ctx, tenant.Tenant{}, "", map[string]string{
		"enabled": "on",
		"queries": "b queries",
	}))
	rowA, err := resA.FetchRow(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "yes", rowA["enabled"])
	assert.Equal(t, "a queries", rowA["queries"], "B's save must not touch A's row")
	assert.Equal(t, "yes", rowA["score_fail_open"])

	// No verified account → fail closed at every entry point.
	_, err = resDeny.FetchRow(ctx, "")
	assert.ErrorIs(t, err, errNoAccount)
	_, err = resDeny.Writer.Load(ctx, tenant.Tenant{}, "")
	assert.ErrorIs(t, err, errNoAccount)
	err = resDeny.Writer.Save(ctx, tenant.Tenant{}, "", map[string]string{"enabled": "on"})
	assert.ErrorIs(t, err, errNoAccount)
	rows, total, err = resDeny.Lister(ctx, resource.ListQuery{})
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, rows)
}
