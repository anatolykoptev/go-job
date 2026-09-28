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

	rowsA, _, err := jobsLister(pool, "test_admin", nil, nil, fixedAccount(aidA))(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsA, 1)
	assert.Contains(t, rowsA[0].Cells[5].Value, ">91", "A's lister renders A's score")

	rowsB, _, err := jobsLister(pool, "test_admin", nil, nil, fixedAccount(aidB))(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsB, 1)
	assert.NotContains(t, rowsB[0].Cells[5].Value, ">91", "B's lister must never render A's score")
	assert.Contains(t, rowsB[0].Cells[5].Value, "fit-unscored", "B renders the unscored chip")

	// Denied resolver → uuid.Nil binds → unscored, never A's row.
	rowsNil, _, err := jobsLister(pool, "test_admin", nil, nil, denyAccount())(ctx, q)
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
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_ratings WHERE entry_id = $1 AND user_name = 'iso_sl_user'`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	require.NoError(t, store.Rate(ctx, "job", jobID, "iso_sl_user", hunt.StageSaved, "", ""))

	authority := applications.New(nil, t.TempDir())
	listerA := shortlistLister(store, "iso_sl_user", authority, nil, fixedAccount(aidA))
	listerB := shortlistLister(store, "iso_sl_user", authority, nil, fixedAccount(aidB))

	q := resource.ListQuery{Sort: shortlistSpec.Resolve("fit", "desc"), Limit: 25}

	rowsA, _, err := listerA(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsA, 1)
	assert.Contains(t, rowsA[0].Cells[4].Value, ">91", "A's shortlist renders A's score")

	rowsB, _, err := listerB(ctx, q)
	require.NoError(t, err)
	require.Len(t, rowsB, 1, "B still sees the membership row (ratings shared)")
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
