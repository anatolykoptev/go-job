package engine

import "github.com/anatolykoptev/go_job/internal/oversize"

// Package-level oversize store singleton, set from main.go.
var oversizeStore *oversize.Store

// SetOversizeStore sets the package-level oversize store instance.
func SetOversizeStore(s *oversize.Store) { oversizeStore = s }

// GetOversizeStore returns the package-level oversize store instance (may be nil).
func GetOversizeStore() *oversize.Store { return oversizeStore }
