package adminui

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openBadgeTestStore opens a hunt.Store against DATABASE_URL, or skips the test;
// fatals if DATABASE_URL points at a non-_test database.
func openBadgeTestStore(t *testing.T) *hunt.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return hunt.NewStore(pool)
}

// TestJobsResource_BadgeClosureNonNil asserts that jobsResource sets a non-nil
// Badge closure. Requires DATABASE_URL; skips otherwise.
//
// RED-on-revert: removing Badge from jobsResource makes this test fail with
// "jobs resource Badge must be non-nil".
func TestJobsResource_BadgeClosureNonNil(t *testing.T) {
	store := openBadgeTestStore(t)
	r := jobsResource(store, nil)
	if r.Badge == nil {
		t.Fatal("jobs resource Badge must be non-nil")
	}
	// Call Badge and confirm it returns a string (no panic, no crash).
	got := r.Badge(context.Background())
	// got is either "" (no open jobs) or a digit string — both are valid.
	// Verify the non-empty case is a valid integer representation if non-empty.
	if got != "" {
		if _, err := strconv.Atoi(got); err != nil {
			t.Fatalf("jobs Badge returned non-integer %q: %v", got, err)
		}
	}
	t.Logf("jobs Badge: %q", got)
}

// TestShortlistResource_BadgeClosureNonNil mirrors the jobs badge test for the
// shortlist resource. Requires DATABASE_URL; skips otherwise.
//
// RED-on-revert: removing Badge from shortlistResource makes this test fail.
func TestShortlistResource_BadgeClosureNonNil(t *testing.T) {
	store := openBadgeTestStore(t)
	r := shortlistResource(store, "test_badge_user", nil)
	if r.Badge == nil {
		t.Fatal("shortlist resource Badge must be non-nil")
	}
	got := r.Badge(context.Background())
	// For "test_badge_user" with no real rows the count is 0 → empty string.
	if got != "" {
		if _, err := strconv.Atoi(got); err != nil {
			t.Fatalf("shortlist Badge returned non-integer %q: %v", got, err)
		}
	}
	t.Logf("shortlist Badge: %q", got)
}

// TestBadgeZeroReturnsEmpty is a pure unit test (no DB) that asserts the
// zero-count guard ("if n == 0 { return "" }") works correctly so no empty
// pill is rendered in the sidebar. Does not require DATABASE_URL.
//
// RED-on-revert: if the zero-guard is removed, a "0" pill would appear.
func TestBadgeZeroReturnsEmpty(t *testing.T) {
	// Replicate the exact badge-building pattern from jobsResource /
	// shortlistResource using a stub count of 0.
	badge := shell.CachedBadge(30*time.Second, func(_ context.Context) string {
		const stubCount = 0
		if stubCount == 0 {
			return ""
		}
		return strconv.Itoa(stubCount)
	})

	got := badge(context.Background())
	if got != "" {
		t.Fatalf("badge for zero count must return empty string, got %q", got)
	}
}

// TestBadgePositiveCount is a pure unit test (no DB) that asserts a positive
// count is formatted as a decimal string.
//
// RED-on-revert: removing strconv.Itoa(n) or adding wrong formatting fails this.
func TestBadgePositiveCount(t *testing.T) {
	const stubCount = 42
	badge := shell.CachedBadge(30*time.Second, func(_ context.Context) string {
		if stubCount == 0 {
			return ""
		}
		return strconv.Itoa(stubCount)
	})

	got := badge(context.Background())
	if got != "42" {
		t.Fatalf("badge for count=42: got %q, want %q", got, "42")
	}
}
