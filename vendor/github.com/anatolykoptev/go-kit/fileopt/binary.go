package fileopt

import (
	"os"
	"os/exec"
)

// resolveBinary resolves a system binary path. Order of precedence:
//  1. The envVar, if set and non-empty (useful in tests and per-host overrides).
//  2. PATH lookup via exec.LookPath.
//  3. Empty string if not found. Callers MUST handle empty as "skip".
//
// This mirrors the resolveMarkitdownBinary pattern in pkg/contentproc.
func resolveBinary(envVar, name string) string {
	if v := lookupEnv(envVar); v != "" {
		return v
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// binaryExists reports whether the file at path exists and is a regular file.
// Used to validate env-overridden paths that bypass PATH lookup.
func binaryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// lookupEnv is separated for test injection. os.Getenv returns "" for unset;
// we collapse to "" so the caller does a single check.
func lookupEnv(name string) string {
	return osGetenv(name)
}
