package jobs

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/anatolykoptev/go-kit/uploads"
)

// profilePath resolves the user-profile file under the canonical uploads base
// ($UPLOADS_ROOT/go-job/profile/profile.json), so a read-only container root
// (HOME=/root, mounted RO) does not break profile persistence — the operator
// points UPLOADS_ROOT at a mounted writable volume. Search preferences are the
// only file-backed state left here; tracked jobs moved to Postgres in ADR-002.
func profilePath() (string, error) {
	return uploads.Path("go-job", "profile", "profile.json")
}

// UserProfile stores user preferences for job search.
type UserProfile struct {
	Blacklist       string `json:"blacklist,omitempty"`
	DefaultPlatform string `json:"default_platform,omitempty"`
	DefaultLimit    int    `json:"default_limit,omitempty"`
	DefaultLocation string `json:"default_location,omitempty"`
	DefaultRemote   string `json:"default_remote,omitempty"`
}

var (
	cachedProfile *UserProfile
	profileOnce   sync.Once
)

// LoadProfile loads the user profile from $UPLOADS_ROOT/go-job/profile/profile.json.
// Returns an empty profile if the file doesn't exist. Cached after first load.
func LoadProfile() *UserProfile {
	profileOnce.Do(func() {
		cachedProfile = &UserProfile{}
		path, err := profilePath()
		if err != nil {
			return
		}
		data, err := os.ReadFile(path) //nolint:gosec // path is service-controlled (uploads base), not user input
		if err != nil {
			return
		}
		_ = json.Unmarshal(data, cachedProfile)
	})
	return cachedProfile
}

// SaveProfile writes the user profile to $UPLOADS_ROOT/go-job/profile/profile.json.
// uploads.Path creates the parent bucket directory, so no separate MkdirAll is
// needed; the base must be writable (mounted volume), not the read-only root FS.
func SaveProfile(p *UserProfile) error {
	path, err := profilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
