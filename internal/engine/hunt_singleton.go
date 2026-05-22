package engine

import "github.com/anatolykoptev/go_job/internal/hunt"

// Package-level hunt store singleton, set from main.go.
var huntStore *hunt.Store

// SetHuntStore sets the package-level hunt store instance.
func SetHuntStore(s *hunt.Store) { huntStore = s }

// GetHuntStore returns the package-level hunt store instance (may be nil).
func GetHuntStore() *hunt.Store { return huntStore }
