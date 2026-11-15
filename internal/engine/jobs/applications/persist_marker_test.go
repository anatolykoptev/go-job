package applications_test

import (
	"context"
	"errors"
	"os"
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
