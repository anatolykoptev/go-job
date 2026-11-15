package main

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/google/uuid"
)

// TestWireArtifactMarker_PersistCallsMarker: with retention on, the production
// wiring must make Persist mark the job. Mutation: delete the SetJobMarker
// call in wireArtifactMarker and this goes RED (Persist returns ErrNoJobMarker
// because RequireJobMarker is set, and the marker is never invoked).
func TestWireArtifactMarker_PersistCallsMarker(t *testing.T) {
	t.Setenv("UPLOADS_ROOT", t.TempDir())
	aid := uuid.New()
	var got int64
	auth := applications.New(nil, "", aid)
	wireArtifactMarker(auth, func(_ context.Context, _ uuid.UUID, id int64) error { got = id; return nil }, 90)
	if _, err := auth.ForAccount(aid).Persist(context.Background(), 31, "# r", "# c"); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if got != 31 {
		t.Fatalf("marker not called for job 31 (got %d)", got)
	}
}

// Retention off wires nothing: Persist neither requires nor calls a marker.
func TestWireArtifactMarker_DisabledWiresNothing(t *testing.T) {
	t.Setenv("UPLOADS_ROOT", t.TempDir())
	aid := uuid.New()
	called := false
	auth := applications.New(nil, "", aid)
	wireArtifactMarker(auth, func(context.Context, uuid.UUID, int64) error { called = true; return nil }, 0)
	if _, err := auth.ForAccount(aid).Persist(context.Background(), 32, "# r", "# c"); err != nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
