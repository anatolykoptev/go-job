package applications_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/google/uuid"
)

// Persist marks the job (account + id) BEFORE writing files; a marker failure
// aborts with nothing on disk.
func TestPersist_MarksJobBeforeWriting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)
	aid := uuid.New()
	var gotAID uuid.UUID
	var gotID int64
	auth := applications.New(nil, "", aid)
	auth.SetJobMarker(func(_ context.Context, a uuid.UUID, id int64) error {
		gotAID, gotID = a, id
		return nil
	})
	if _, err := auth.ForAccount(aid).Persist(context.Background(), 77, "# r", "# c"); err != nil {
		t.Fatal(err)
	}
	if gotAID != aid || gotID != 77 {
		t.Fatalf("marker got (%s, %d), want (%s, 77)", gotAID, gotID, aid)
	}

	root2 := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root2)
	fail := applications.New(nil, "", aid)
	fail.SetJobMarker(func(context.Context, uuid.UUID, int64) error { return errors.New("db down") })
	if _, err := fail.ForAccount(aid).Persist(context.Background(), 78, "# r", "# c"); err == nil {
		t.Fatal("marker failure must fail Persist")
	}
	if entries, _ := os.ReadDir(root2); len(entries) != 0 {
		t.Fatalf("nothing may be written when marking fails, found %v", entries)
	}
}

// With retention on (RequireJobMarker) a missing marker is a loud error, not
// silently unmarked artifacts.
func TestPersist_RequiredMarkerMissingFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)
	aid := uuid.New()
	auth := applications.New(nil, "", aid)
	auth.RequireJobMarker()
	if _, err := auth.ForAccount(aid).Persist(context.Background(), 5, "# r", "# c"); !errors.Is(err, applications.ErrNoJobMarker) {
		t.Fatalf("want ErrNoJobMarker, got %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("nothing may be written, found %v", entries)
	}
}

// Fail closed: an unreadable artifact store or legacy dir is an error, never
// "no artifacts"; only a missing uploads base counts as empty.
func TestArtifactScan_FailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are meaningless as root")
	}
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)
	op := uuid.New()
	auth := applications.New(nil, "", op)

	if _, _, err := auth.ArtifactJobRefs(); err != nil {
		t.Fatalf("missing uploads base is not an error: %v", err)
	}
	base := filepath.Join(root, "go-job", "applications")
	if err := os.MkdirAll(filepath.Join(base, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	aids, ids, err := auth.ArtifactJobRefs()
	if err != nil || len(ids) != 1 || ids[0] != 42 || aids[0] != op {
		t.Fatalf("legacy numeric dir: aids=%v ids=%v err=%v", aids, ids, err)
	}
	if err := os.Chmod(base, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })
	if _, _, err := auth.ArtifactJobRefs(); err == nil {
		t.Fatal("unreadable artifact base must be an error")
	}

	// Legacy: disabled -> nil; configured+empty -> error; configured+missing -> error.
	if m, _, err := applications.New(nil, "", op).LegacyMatcher(); m != nil || err != nil {
		t.Fatalf("legacy disabled: want nil,nil got %v,%v", m != nil, err)
	}
	if _, _, err := applications.New(nil, t.TempDir(), op).LegacyMatcher(); err == nil {
		t.Fatal("configured but empty legacy dir must be an error")
	}
	if _, _, err := applications.New(nil, filepath.Join(root, "absent"), op).LegacyMatcher(); err == nil {
		t.Fatal("configured but unreadable legacy dir must be an error")
	}
	ld := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ld, "anthropic-databases"), 0o755)
	m, n, err := applications.New(nil, ld, op).LegacyMatcher()
	if err != nil || n != 1 || !m("Anthropic") || m("Anthropic Labs") || m("") {
		t.Fatalf("company match wrong: n=%d err=%v", n, err)
	}
}
