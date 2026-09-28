package adminui

// deny_matrix_test.go — the consolidated P5 deny matrix (plan ADR-13/ADR-15):
// ONE test enumerates every account-owned surface and asserts account B can
// never read or write account A's rows, and that uuid.Nil / a request
// carrying no verified identity fails closed.
//
// Per-surface deep matrices live next to each store and are NOT duplicated
// here — hunt.TestAccountStore_DenyMatrix, jobs.TestPerson* / TestVector* /
// TestGraphAccountIsolation, oversize.TestStore_AccountIsolation, and the
// lister-level pins in account_isolation_test.go. This file is the
// enumeration that proves no surface was skipped, exercised at the seam the
// request actually crosses (adminui's accountResolver / jobserver's
// accounts.AccountFrom / the ForAccount facades).
//
// RED-on-revert: any surface that drops its account predicate, re-opens an
// unscoped read, or starts trusting an unverified ctx fails exactly one
// numbered leg below.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/jobserver"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDenyMatrix_AccountSurfaces(t *testing.T) {
	pool := openJobsPool(t)
	dbtest.RequireTestDB(t, os.Getenv("DATABASE_URL")) // fitness gate: this file opens ConnectResumeDB
	ctx := context.Background()

	// Every store seam the matrix touches, migrated on the shared test DB.
	huntStore := hunt.NewStore(pool)
	require.NoError(t, huntStore.Migrate(ctx))
	rdb, err := jobs.ConnectResumeDB(ctx, os.Getenv("DATABASE_URL"))
	require.NoError(t, err, "ConnectResumeDB")
	defer rdb.Close()
	jobs.SetResumeDB(rdb)
	t.Cleanup(func() { jobs.SetResumeDB(nil) })
	oStore := oversize.NewStore(pool)
	require.NoError(t, oStore.Migrate(ctx))
	engine.SetOversizeStore(oStore)
	engine.SetHuntStore(huntStore)
	t.Cleanup(func() {
		engine.SetOversizeStore(nil)
		engine.SetHuntStore(nil)
	})

	aidA := newTestAccount(t, pool)
	aidB := newTestAccount(t, pool)
	acctA := huntStore.ForAccount(aidA)
	acctB := huntStore.ForAccount(aidB)
	acctNil := huntStore.ForAccount(uuid.Nil)

	// One shared corpus job — score/rating fixtures hang off it.
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, status)
		VALUES ($1, 'DenyMatrix Role', 'DenyCorp', 'https://deny.example/jobs/matrix', 'deny_mx', 'open')
		ON CONFLICT (dedup_hash) DO UPDATE SET status='open'
		RETURNING id`, hunt.DedupHash("https://deny.example/jobs/matrix")).Scan(&jobID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_job_scores WHERE job_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_ratings WHERE entry_id = $1`, jobID)
		_, _ = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	})

	// ── 1. job scores (account_job_scores) ────────────────────────────────
	t.Run("job_scores", func(t *testing.T) {
		require.NoError(t, acctA.SetJobScore(ctx, jobID, hunt.ScoreResult{
			FitScore:    77,
			FitBand:     "strong",
			SuccessBand: "STRONG",
			ScoredAt:    time.Now().UTC().Truncate(time.Second),
		}))
		recA, err := scanJobDetail(ctx, pool, jobID, aidA)
		require.NoError(t, err)
		require.NotNil(t, recA.FitScore, "A reads its own score")
		assert.Equal(t, 77, *recA.FitScore)

		recB, err := scanJobDetail(ctx, pool, jobID, aidB)
		require.NoError(t, err)
		assert.Nil(t, recB.FitScore, "B must not read A's score")
		recNil, err := scanJobDetail(ctx, pool, jobID, uuid.Nil)
		require.NoError(t, err)
		assert.Nil(t, recNil.FitScore, "no-identity renders unscored, never a leak")
	})

	// ── 2. ratings + shortlist (hunt_ratings) ─────────────────────────────
	t.Run("ratings_and_shortlist", func(t *testing.T) {
		require.NoError(t, acctA.Rate(ctx, "job", jobID, hunt.StageSaved, "", "a-only"))
		_, err := acctB.GetRating(ctx, "job", jobID)
		assert.ErrorIs(t, err, hunt.ErrNotFound, "B must not read A's rating")
		_, err = acctNil.GetRating(ctx, "job", jobID)
		assert.ErrorIs(t, err, hunt.ErrNotFound, "uuid.Nil reads no rating")

		// B's own write lands under B and never disturbs A's row.
		require.NoError(t, acctB.Rate(ctx, "job", jobID, hunt.StageSaved, "", "b-only"))
		ratA, err := acctA.GetRating(ctx, "job", jobID)
		require.NoError(t, err)
		assert.Equal(t, hunt.StageSaved, ratA.Triage)
		assert.Equal(t, "a-only", ratA.Note, "B's write must not touch A's row")

		// Edge lister: B's shortlist holds only B's own rating row.
		authority := applications.New(nil, t.TempDir(), aidA)
		q := resource.ListQuery{Sort: shortlistSpec.Resolve("fit", "desc"), Limit: 25}
		rowsB, _, err := shortlistLister(huntStore, authority, nil, fixedAccount(aidB))(ctx, q)
		require.NoError(t, err)
		require.Len(t, rowsB, 1)
		assert.Contains(t, rowsB[0].Cells[4].Value, "fit-unscored",
			"B's shortlist chip is unscored — A's score unreachable")
		rowsNil, totalNil, err := shortlistLister(huntStore, authority, nil, denyAccount())(ctx, q)
		require.NoError(t, err)
		assert.Zero(t, totalNil)
		assert.Empty(t, rowsNil, "no-identity shortlist is fail-closed empty")
	})

	// ── 3. hunt settings (account_hunt_settings) ──────────────────────────
	t.Run("hunt_settings", func(t *testing.T) {
		resA := huntSettingsResource(huntStore, fixedAccount(aidA))
		resB := huntSettingsResource(huntStore, fixedAccount(aidB))
		resNil := huntSettingsResource(huntStore, denyAccount())

		require.NoError(t, resA.Writer.Save(ctx, tenant.Tenant{}, "", map[string]string{
			"enabled": "on",
			"queries": "a-only queries",
		}))
		rowB, err := resB.FetchRow(ctx, "")
		require.NoError(t, err)
		assert.Equal(t, "", rowB["enabled"], "B must never read A's armed row")
		assert.Equal(t, "", rowB["queries"])

		_, err = resNil.FetchRow(ctx, "")
		assert.ErrorIs(t, err, errNoAccount)
		err = resNil.Writer.Save(ctx, tenant.Tenant{}, "", map[string]string{"enabled": "on"})
		assert.ErrorIs(t, err, errNoAccount, "no-identity settings write must deny")
	})

	// ── 4. resume person + children (resume_persons hub) ──────────────────
	t.Run("resume_person_children", func(t *testing.T) {
		ra, rb := rdb.ForAccount(aidA), rdb.ForAccount(aidB)
		pidA, err := ra.InsertPerson(ctx, jobs.PersonRecord{Name: "Deny Person A", Email: "deny-a@example.com"})
		require.NoError(t, err)
		pidB, err := rb.InsertPerson(ctx, jobs.PersonRecord{Name: "Deny Person B"})
		require.NoError(t, err)
		expA, err := ra.InsertExperience(ctx, pidA, jobs.ExperienceRecord{Title: "A Role", Company: "ACo"})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = ra.ClearPerson(ctx, pidA)
			_ = rb.ClearPerson(ctx, pidB)
		})

		_, err = rb.GetPerson(ctx, pidA)
		assert.Error(t, err, "B must not read A's person")
		_, err = rb.InsertExperience(ctx, pidA, jobs.ExperienceRecord{Title: "evil", Company: "evil"})
		assert.Error(t, err, "B must not write under A's person")
		_, err = rb.GetExperienceByID(ctx, expA)
		assert.Error(t, err, "B must not read A's child row")

		// The adminui resource edge denies at the same boundary.
		pResB := personsResource(pool, fixedAccount(aidB))
		_, err = pResB.FetchRow(ctx, strconv.Itoa(pidA))
		assert.ErrorIs(t, err, resource.ErrDetailNotFound)
		eResB := experiencesResource(pool, fixedAccount(aidB))
		_, err = eResB.FetchRow(ctx, strconv.Itoa(expA))
		assert.ErrorIs(t, err, resource.ErrDetailNotFound)
		pResNil := personsResource(pool, denyAccount())
		_, err = pResNil.FetchRow(ctx, strconv.Itoa(pidA))
		assert.ErrorIs(t, err, resource.ErrDetailNotFound, "no-identity person read denies")

		// The nil facade fails closed on writes outright.
		_, err = rdb.ForAccount(uuid.Nil).InsertPerson(ctx, jobs.PersonRecord{Name: "nobody"})
		assert.ErrorIs(t, err, jobs.ErrNoAccountScope)
	})

	// ── 5. vectors (resume_vectors) ───────────────────────────────────────
	t.Run("vectors", func(t *testing.T) {
		ra, rb := rdb.ForAccount(aidA), rdb.ForAccount(aidB)
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx,
				`DELETE FROM resume_vectors WHERE account_id IN ($1, $2) AND content LIKE 'deny-matrix%'`,
				aidA, aidB)
		})
		_, err := ra.UpsertVector(ctx, "deny-matrix A secret note", "note", nil)
		require.NoError(t, err)

		cnt, err := rb.CountVectors(ctx, "note")
		require.NoError(t, err)
		assert.Zero(t, cnt, "B's vector count excludes A's rows")
		found, err := rb.SearchByText(ctx, "deny-matrix secret", 10)
		require.NoError(t, err)
		for _, row := range found {
			assert.NotContains(t, row.Content, "deny-matrix", "B must never see A's vector content")
		}
		_, err = rdb.ForAccount(uuid.Nil).UpsertVector(ctx, "x", "note", nil)
		assert.ErrorIs(t, err, jobs.ErrNoAccountScope)
	})

	// ── 6. oversize (oversize_responses) ──────────────────────────────────
	t.Run("oversize", func(t *testing.T) {
		idA, err := oStore.ForAccount(aidA).Save(ctx, oversize.Entry{
			ToolName: "deny_mx", Payload: json.RawMessage(`{"k":1}`),
			SizeBytes: 7, SHA256: "deny-mx-a",
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM oversize_responses WHERE id = $1`, idA)
		})

		_, err = oStore.ForAccount(aidB).Get(ctx, idA)
		assert.ErrorIs(t, err, oversize.ErrNotFound, "B must not read A's spill")
		_, err = oStore.ForAccount(uuid.Nil).Get(ctx, idA)
		assert.ErrorIs(t, err, oversize.ErrNotFound, "uuid.Nil reads no spill")

		q := resource.ListQuery{Sort: oversizeSpec.Resolve("created", "desc"), Limit: 25}
		_, totalB, err := oversizeResource(pool, fixedAccount(aidB)).Lister(ctx, q)
		require.NoError(t, err)
		assert.Zero(t, totalB, "B's oversize listing must not contain A's row")
		_, totalNil, err := oversizeResource(pool, denyAccount()).Lister(ctx, q)
		require.NoError(t, err)
		assert.Zero(t, totalNil, "no-identity oversize listing is empty")
	})

	// ── 7. downloads (applications fs) ────────────────────────────────────
	t.Run("downloads", func(t *testing.T) {
		uploadsRoot := t.TempDir()
		t.Setenv("UPLOADS_ROOT", uploadsRoot)
		dlJob := int64(98765)
		dirA := filepath.Join(uploadsRoot, "go-job", "applications", aidA.String(), strconv.FormatInt(dlJob, 10))
		require.NoError(t, os.MkdirAll(dirA, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dirA, "resume.pdf"), []byte("%PDF-deny"), 0o644))

		authority := applications.New(nil, "", aidA) // aidA plays operator
		get := func(h http.HandlerFunc) int {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet,
				"/admin/jobs/"+strconv.FormatInt(dlJob, 10)+"/download/resume", nil)
			req.SetPathValue("id", strconv.FormatInt(dlJob, 10))
			req.SetPathValue("kind", "resume")
			rr := httptest.NewRecorder()
			h(rr, req)
			return rr.Code
		}
		assert.Equal(t, http.StatusOK, get(downloadHandler(pool, authority, fixedAccount(aidA))))
		assert.Equal(t, http.StatusNotFound, get(downloadHandler(pool, authority, fixedAccount(aidB))),
			"B must not fetch A's artifact even with a known job id")
		assert.Equal(t, http.StatusNotFound, get(downloadHandler(pool, authority, denyAccount())),
			"no-identity download denies")
	})

	// ── 8. MCP tools deny without identity ────────────────────────────────
	// Every tool that touches account-owned data gates on
	// accounts.AccountFrom; a bare ctx (no bcrypt session, no verified
	// bearer TokenInfo) must deny on every one of them.
	t.Run("mcp_tools_deny_without_identity", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "deny-mx", Version: "t"}, nil)
		jobserver.RegisterTools(srv, applications.New(nil, "", uuid.Nil))
		serverT, clientT := mcp.NewInMemoryTransports()
		mctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = srv.Run(mctx, serverT) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "deny-mx-client", Version: "t"}, nil)
		cs, err := client.Connect(mctx, clientT, nil)
		require.NoError(t, err)
		defer func() { _ = cs.Close() }()

		tools := []struct {
			name string
			args map[string]any
		}{
			{"resume_memory", map[string]any{"op": "search", "query": "x"}},
			{"resume_profile", map[string]any{}},
			{"resume_profile_sync", map[string]any{}},
			{"master_resume_build", map[string]any{"resume": "x"}},
			{"resume_generate", map[string]any{"job_description": "x"}},
			{"resume_enrich", map[string]any{"action": "start"}},
			{"application_persist", map[string]any{"job_id": 1, "resume_md": "x", "cover_md": "y"}},
			{"oversize", map[string]any{"op": "list"}},
		}
		for _, tc := range tools {
			res, err := cs.CallTool(mctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				continue // handler error — denied (identity gate fired)
			}
			require.True(t, res.IsError,
				"MCP tool %s must deny with no account identity — got a non-error result", tc.name)
		}
	})

	// ── 9. AGE graph — the surface no in-process test can reach ───────────
	// Apache AGE is absent on the test cluster, so the cypher layer cannot
	// execute here; jobs.TestGraphAccountIsolation covers it when the
	// extension exists. The structural invariant is asserted at the SQL
	// layer instead: EVERY ag_catalog.cypher call site in resumedb_graph.go
	// must carry the bound account's `aid` property, and the graph API may
	// only hang off the account-bound *ResumeAccount facade — never
	// *ResumeDB directly (an unbound receiver would be an unscoped cypher).
	t.Run("graph_cypher_scope_source", func(t *testing.T) {
		src, err := os.ReadFile("../engine/jobs/resumedb_graph.go")
		require.NoError(t, err)
		s := string(src)
		assert.NotContains(t, s, "func (db *ResumeDB)",
			"graph entry points must hang off the account-bound ResumeAccount facade only")
		chunks := strings.Split(s, "ag_catalog.cypher(")
		require.Greater(t, len(chunks)-1, 5, "expected multiple cypher call sites")
		for i, c := range chunks[1:] {
			end := 600
			if len(c) < end {
				end = len(c)
			}
			assert.Contains(t, c[:end], "aid",
				"cypher call site %d must scope by the bound account's aid", i+1)
		}
	})
}
