// Package env provides typed access to environment variables with defaults.
// Zero external dependencies. Designed to replace duplicated env/envInt/envList
// helpers found across go-* services.
package env

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Source provides environment variable lookups. Replace DefaultSource
// for testing or to read from alternative sources.
type Source interface {
	Lookup(key string) (string, bool)
}

type osSource struct{}

func (osSource) Lookup(key string) (string, bool) { return os.LookupEnv(key) }

// DefaultSource is the global source for all env functions.
// Defaults to OSSource (reads from os.LookupEnv).
// Replace with MapSource in tests for parallel-safe, isolated testing.
var DefaultSource Source = osSource{}

type mapSource map[string]string

// MapSource returns a Source backed by a map. Use in tests:
//
//	env.DefaultSource = env.MapSource(map[string]string{"KEY": "value"})
func MapSource(m map[string]string) Source {
	return mapSource(m)
}

func (ms mapSource) Lookup(key string) (string, bool) {
	v, ok := ms[key]
	return v, ok
}

// getenv returns the value from DefaultSource, or "" if not set.
func getenv(key string) string {
	v, _ := DefaultSource.Lookup(key)
	return v
}

// Lookup returns the value of the environment variable and whether it was set.
// Unlike Str, it distinguishes between "not set" and "set to empty string".
func Lookup(key string) (string, bool) {
	return DefaultSource.Lookup(key)
}

// Exists reports whether the environment variable is set (even if empty).
func Exists(key string) bool {
	_, ok := DefaultSource.Lookup(key)
	return ok
}

// Required returns the value of the environment variable named by key.
// Returns NotSetError if the variable is not set or is empty.
// Use this for variables that must be configured (e.g. DATABASE_URL).
func Required(key string) (string, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return "", &NotSetError{Key: key}
	}
	return v, nil
}

// Str returns the value of the environment variable named by key,
// or def if the variable is not set or empty.
func Str(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// Int returns the environment variable as an int, or def if not set or invalid.
func Int(key string, def int) int {
	if v := getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// IntE is like Int but returns a ParseError if the variable is set but not a valid integer.
// If the variable is not set, returns (def, nil).
func IntE(key string, def int) (int, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "int", Err: err}
	}
	return n, nil
}

// Int64 returns the environment variable as int64, or def if not set or invalid.
func Int64(key string, def int64) int64 {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Int64E is like Int64 but returns a ParseError if the variable is set but not a valid int64.
func Int64E(key string, def int64) (int64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "int64", Err: err}
	}
	return n, nil
}

// Float returns the environment variable as float64, or def if not set or invalid.
func Float(key string, def float64) float64 {
	if v := getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// FloatE is like Float but returns a ParseError if the variable is set but not a valid float64.
func FloatE(key string, def float64) (float64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "float64", Err: err}
	}
	return f, nil
}

// Uint returns the environment variable as a uint, or def if not set or invalid.
func Uint(key string, def uint) uint {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, strconv.IntSize); err == nil {
			return uint(n)
		}
	}
	return def
}

// UintE is like Uint but returns a ParseError if the variable is set but not a valid uint.
func UintE(key string, def uint) (uint, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, strconv.IntSize)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "uint", Err: err}
	}
	return uint(n), nil
}

// Uint64 returns the environment variable as uint64, or def if not set or invalid.
func Uint64(key string, def uint64) uint64 {
	if v := getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Uint64E is like Uint64 but returns a ParseError if the variable is set but not a valid uint64.
func Uint64E(key string, def uint64) (uint64, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "uint64", Err: err}
	}
	return n, nil
}

// Bool returns the environment variable as a bool, or def if not set.
// Truthy: "true", "1", "yes" (case-insensitive).
// Falsy: "false", "0", "no" (case-insensitive).
// Anything else returns def.
func Bool(key string, def bool) bool {
	v := getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

// BoolE is like Bool but returns a ParseError if the variable is set
// to an unrecognized value (not true/1/yes/false/0/no).
func BoolE(key string, def bool) (bool, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return def, &ParseError{Key: key, Value: v, Type: "bool"}
	}
}

// Duration returns the environment variable parsed as a duration.
// Accepts Go duration strings ("5s", "100ms", "2m30s") and float seconds ("3.5" -> 3.5s).
// Returns def if not set or invalid.
func Duration(key string, def time.Duration) time.Duration {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def
	}
	// Try Go duration format first.
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Fall back to float seconds for backward compat.
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second))
	}
	return def
}

// DurationE is like Duration but returns a ParseError if the variable is set
// but cannot be parsed. Accepts Go duration strings ("5s", "100ms", "2m30s")
// and falls back to float seconds ("3.5" -> 3.5s) for backward compatibility.
func DurationE(key string, def time.Duration) (time.Duration, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	// Try Go duration format first ("5s", "100ms", "2m30s").
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	// Fall back to float seconds for backward compat ("3.5" -> 3.5s).
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return def, &ParseError{Key: key, Value: v, Type: "duration"}
}

// List returns a comma-separated environment variable as a trimmed string slice.
// Empty entries are dropped. Returns nil if the variable is not set and def is "".
func List(key, def string) []string {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Int64List returns a comma-separated list of int64 values.
// Non-numeric entries are silently skipped. Returns nil if not set.
func Int64List(key string) []int64 {
	v := getenv(key)
	if v == "" {
		return nil
	}
	var result []int64
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			result = append(result, n)
		}
	}
	return result
}

// Map returns a comma-separated list of key:value pairs as a map.
// Format: "k1:v1,k2:v2". Entries without ":" are silently skipped.
// Whitespace is trimmed from keys and values. Returns nil if not set.
func Map(key, def string) map[string]string {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		m[k] = strings.TrimSpace(val)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// URL returns the environment variable parsed as a URL.
// Returns the parsed def if the variable is not set or invalid.
// Returns nil if both the variable and def are empty.
func URL(key string, def string) *url.URL {
	v := Str(key, def)
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		if def != "" {
			u, _ = url.Parse(def)
			return u
		}
		return nil
	}
	return u
}

// URLE is like URL but returns a ParseError if the variable is set but not a valid URL.
// If the variable is not set, returns the parsed def.
func URLE(key string, def string) (*url.URL, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		if def == "" {
			return nil, nil
		}
		u, _ := url.Parse(def)
		return u, nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, &ParseError{Key: key, Value: v, Type: "url", Err: err}
	}
	return u, nil
}

// File reads a file path from the environment variable and returns its contents.
// Trailing newlines are trimmed. Returns def if not set or file cannot be read.
// Useful for Docker secrets (/run/secrets/) and Kubernetes secret volumes.
func File(key, def string) string {
	path := getenv(key)
	if path == "" {
		return def
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	return strings.TrimRight(string(data), "\n")
}

// FileE is like File but returns an error if the variable is not set
// or the file cannot be read.
func FileE(key string) (string, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return "", &NotSetError{Key: key}
	}
	data, err := os.ReadFile(v)
	if err != nil {
		return "", &ParseError{Key: key, Value: v, Type: "file", Err: err}
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// Expand returns the environment variable with ${VAR} references expanded.
// Uses os.Expand with the current DefaultSource for variable resolution.
// Returns def if the variable is not set.
func Expand(key, def string) string {
	v := Str(key, def)
	if v == "" {
		return ""
	}
	return os.Expand(v, getenv)
}

// Base64 returns the environment variable decoded from standard base64.
// Returns def if not set or invalid.
func Base64(key string, def []byte) []byte {
	v := getenv(key)
	if v == "" {
		return def
	}
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return def
	}
	return data
}

// Base64E is like Base64 but returns a ParseError on invalid base64.
func Base64E(key string, def []byte) ([]byte, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "base64", Err: err}
	}
	return data, nil
}

// Hex returns the environment variable decoded from hex.
// Returns def if not set or invalid.
func Hex(key string, def []byte) []byte {
	v := getenv(key)
	if v == "" {
		return def
	}
	data, err := hex.DecodeString(v)
	if err != nil {
		return def
	}
	return data
}

// HexE is like Hex but returns a ParseError on invalid hex.
func HexE(key string, def []byte) ([]byte, error) {
	v, ok := DefaultSource.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	data, err := hex.DecodeString(v)
	if err != nil {
		return def, &ParseError{Key: key, Value: v, Type: "hex", Err: err}
	}
	return data, nil
}
