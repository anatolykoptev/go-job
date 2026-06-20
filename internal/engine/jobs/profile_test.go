package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProfilePath_UnderUploadsRoot is the regression guard for the read-only-FS
// class: the user profile MUST resolve under $UPLOADS_ROOT (a writable mounted
// volume), NOT under $HOME (which is /root and read-only in the container).
// Before this fix, SaveProfile wrote to $HOME/.go_job and failed with
// "mkdir /root/.go_job: read-only file system" — the same class as the tracker.
func TestProfilePath_UnderUploadsRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	got, err := profilePath()
	if err != nil {
		t.Fatalf("profilePath() error: %v", err)
	}
	want := filepath.Join(root, "go-job", "profile", "profile.json")
	if got != want {
		t.Fatalf("profilePath() = %q, want %q", got, want)
	}
	if strings.Contains(got, "/root/") || strings.Contains(got, ".go_job") {
		t.Errorf("profilePath() = %q still references the read-only HOME base", got)
	}
}

// TestSaveProfile_PersistsToUploadsBase proves SaveProfile actually writes to
// the writable uploads base (not the read-only HOME). We assert the persisted
// bytes at the resolved path directly rather than via LoadProfile, because
// profileOnce memoizes the first load within the process and would mask the
// on-disk write. Path resolution is covered separately by
// TestProfilePath_UnderUploadsRoot.
func TestSaveProfile_PersistsToUploadsBase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	p := &UserProfile{
		DefaultPlatform: "remoteok",
		DefaultLimit:    25,
		DefaultLocation: "San Francisco",
		DefaultRemote:   "remote",
	}
	if err := SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile error (read-only base regression?): %v", err)
	}

	path := filepath.Join(root, "go-job", "profile", "profile.json")
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("profile not persisted at %q: %v", path, err)
	}
	for _, want := range []string{"remoteok", "San Francisco", "remote"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("persisted profile missing %q; got: %s", want, data)
		}
	}
}
