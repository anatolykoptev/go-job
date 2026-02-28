package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

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

// LoadProfile loads user profile from ~/.go_job/profile.json.
// Returns empty profile if file doesn't exist. Cached after first load.
func LoadProfile() *UserProfile {
	profileOnce.Do(func() {
		cachedProfile = &UserProfile{}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		data, err := os.ReadFile(filepath.Join(home, ".go_job", "profile.json"))
		if err != nil {
			return
		}
		_ = json.Unmarshal(data, cachedProfile)
	})
	return cachedProfile
}

// SaveProfile writes user profile to ~/.go_job/profile.json.
func SaveProfile(p *UserProfile) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".go_job")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "profile.json"), data, 0o600)
}
