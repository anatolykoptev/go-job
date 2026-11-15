package hunt_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleUnratedJob inserts a job unseen for 200 days with no rating.
func staleUnratedJob(t *testing.T, s *hunt.Store, name, company, title string) int64 {
	t.Helper()
	u := "https://artifacts.example/" + name
	id, _, err := s.UpsertJob(context.Background(), hunt.Job{
		DedupHash: hunt.DedupHash(u), Title: title, Company: company, URL: u, Source: "art"})
	require.NoError(t, err)
	ageRow(t, s.Pool(), "hunt_jobs", id, 200)
	return id
}

func artifactFixture(t *testing.T) (*hunt.Store, *hunt.AccountStore) {
	t.Helper()
	pool := openTestPool(t)
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(context.Background()))
	acc := s.ForAccount(newScoreAccount(t, pool))
	truncateJobs(t, pool)
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_ratings")
	require.NoError(t, err)
	return s, acc
}

// TestPersist_MarkedJobSurvivesPrune is the end-to-end guard: generating an
// application through the real Authority.Persist must mark the job so the
// prune keeps it. Mutation: delete the marker call in Persist
// (applications.go) and this test goes RED (the job is pruned, files orphaned).
func TestPersist_MarkedJobSurvivesPrune(t *testing.T) {
	s, acc := artifactFixture(t)
	t.Setenv("UPLOADS_ROOT", t.TempDir())
	ctx := context.Background()
	marked := staleUnratedJob(t, s, "marked", "Acme", "Eng")
	plain := staleUnratedJob(t, s, "plain", "Acme", "Eng2")

	auth := applications.New(nil, "", acc.AccountID())
	auth.SetJobMarker(func(ctx context.Context, aid uuid.UUID, id int64) error {
		return s.ForAccount(aid).MarkArtifactJob(ctx, id)
	})
	_, err := auth.ForAccount(acc.AccountID()).Persist(ctx, marked, "# resume", "# cover")
	require.NoError(t, err)

	_, err = s.PruneStale(ctx, 90)
	require.NoError(t, err)
	assert.True(t, rowExists(t, s.Pool(), "hunt_jobs", marked), "job with generated artifacts must survive the prune")
	assert.False(t, rowExists(t, s.Pool(), "hunt_jobs", plain), "control: an unmarked stale job is pruned")
	assert.Equal(t, hunt.StageClaimed, readStage(t, s, acc, marked))
}

// A marker must never overwrite a rating the user already set.
func TestMarkArtifactJob_KeepsExistingRating(t *testing.T) {
	s, acc := artifactFixture(t)
	ctx := context.Background()
	id := staleUnratedJob(t, s, "rated", "Acme", "Eng")
	require.NoError(t, acc.SetStage(ctx, hunt.KindJob, id, hunt.StageOffer))
	require.NoError(t, acc.MarkArtifactJob(ctx, id))
	assert.Equal(t, hunt.StageOffer, readStage(t, s, acc, id), "higher stage must not be downgraded")
	assert.NoError(t, acc.MarkArtifactJob(ctx, 999999999), "unknown job id is a no-op, not an error")
}

func TestBackfillArtifactMarkers(t *testing.T) {
	s, acc := artifactFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)
	legacy := t.TempDir()
	operator := acc.AccountID()

	scoped := staleUnratedJob(t, s, "scoped", "Beta", "Eng")
	legacyNum := staleUnratedJob(t, s, "legacynum", "Gamma", "Eng")
	slugJob := staleUnratedJob(t, s, "slug", "Delta", "Platform Engineer")
	noFiles := staleUnratedJob(t, s, "nofiles", "Epsilon", "Eng")

	base := filepath.Join(root, "go-job", "applications")
	require.NoError(t, os.MkdirAll(filepath.Join(base, operator.String(), fmt.Sprint(scoped)), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, fmt.Sprint(legacyNum)), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "delta-platform", "submit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "delta-platform", "submit", "resume.pdf"), []byte("x"), 0o644))

	auth := applications.New(nil, legacy, operator)
	n, err := s.BackfillArtifactMarkers(ctx, 90, auth)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "scoped dir, legacy numeric dir and legacy slug match are marked")

	again, err := s.BackfillArtifactMarkers(ctx, 90, auth)
	require.NoError(t, err)
	assert.Zero(t, again, "backfill is idempotent")

	_, err = s.PruneStale(ctx, 90)
	require.NoError(t, err)
	for name, id := range map[string]int64{"scoped": scoped, "legacy numeric": legacyNum, "legacy slug": slugJob} {
		assert.True(t, rowExists(t, s.Pool(), "hunt_jobs", id), "%s job must survive the prune", name)
	}
	assert.False(t, rowExists(t, s.Pool(), "hunt_jobs", noFiles), "job without artifacts is still pruned")
}
