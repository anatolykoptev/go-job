package engine

// TestFF4_OutcomeVocabularyBounded is the P3 fitness function (FF-4):
//
//  1. validPlatformOutcomes contains exactly the 6 ADR-J3 values and no others.
//  2. PlatformOutcome maps each input class to the right label.
//  3. The sentinel hook functions (isNoAPIKeyErr, isParseErr) default to no-op;
//     after RegisterPlatformOutcomeHooks they classify correctly.
//
// Revert-red guarantee: reverting the validPlatformOutcomes change (back to
// {results,empty,error}) causes assertion #1 to fail (missing ok/timeout/
// no_key/parse_fail) and assertion #2 to fail (PlatformOutcome returns "results"
// not "ok"). Reverting the hook wiring makes #3 fail.

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestFF4_ValidPlatformOutcomesExact asserts the bounded set is exactly the
// 6 ADR-J3 values. A new label accidentally added without updating this test
// would not be caught — but ANY removal or rename will go red here.
func TestFF4_ValidPlatformOutcomesExact(t *testing.T) {
	want := map[string]bool{
		"ok":         true,
		"empty":      true,
		"error":      true,
		"timeout":    true,
		"no_key":     true,
		"parse_fail": true,
	}
	for v := range want {
		if !validPlatformOutcomes[v] {
			t.Errorf("ADR-J3 outcome %q missing from validPlatformOutcomes", v)
		}
	}
	for v := range validPlatformOutcomes {
		if !want[v] {
			t.Errorf("unexpected outcome %q in validPlatformOutcomes (not in ADR-J3 vocabulary)", v)
		}
	}
	// Guard: "results" was the pre-ADR-J3 value; it must NOT appear in the new set.
	if validPlatformOutcomes["results"] {
		t.Error("ADR-J3: old outcome 'results' still present — rename to 'ok' was not applied")
	}
}

// TestFF4_PlatformOutcome_DefaultClassifier verifies PlatformOutcome without hooks.
// Sentinel errors fall through to "error" before hooks are registered.
func TestFF4_PlatformOutcome_DefaultClassifier(t *testing.T) {
	// Save and restore the hook functions so we don't pollute other tests.
	savedNoKey := isNoAPIKeyErr
	savedParse := isParseErr
	defer func() {
		isNoAPIKeyErr = savedNoKey
		isParseErr = savedParse
	}()
	isNoAPIKeyErr = func(error) bool { return false }
	isParseErr = func(error) bool { return false }

	cases := []struct {
		name string
		n    int
		err  error
		want string
	}{
		{"ok", 3, nil, "ok"},
		{"empty", 0, nil, "empty"},
		{"error generic", 0, errors.New("boom"), "error"},
		{"error with results ignored", 1, errors.New("boom"), "error"}, // err takes precedence
		{"timeout DeadlineExceeded", 0, context.DeadlineExceeded, "timeout"},
		{"timeout Canceled", 0, context.Canceled, "timeout"},
		{"wrapped DeadlineExceeded", 0, errors.Join(context.DeadlineExceeded, errors.New("wrap")), "timeout"},
		// Without hooks: sentinel errors fall to "error".
		{"no_key unhooked", 0, errors.New("some api key error"), "error"},
		{"parse_fail unhooked", 0, errors.New("parse error"), "error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlatformOutcome(tc.n, tc.err)
			if got != tc.want {
				t.Errorf("PlatformOutcome(%d, %v) = %q, want %q", tc.n, tc.err, got, tc.want)
			}
		})
	}
}

// errTestNoKey and errTestParse are local test sentinels defined here to avoid
// importing internal/engine/jobs (which would create an import cycle).
var errTestNoKey = errors.New("test: no api key")
var errTestParse = errors.New("test: parse fail")

// TestFF4_PlatformOutcome_WithHooks verifies that after RegisterPlatformOutcomeHooks
// the sentinel errors are classified as no_key and parse_fail respectively.
func TestFF4_PlatformOutcome_WithHooks(t *testing.T) {
	// Save and restore.
	savedNoKey := isNoAPIKeyErr
	savedParse := isParseErr
	defer func() {
		isNoAPIKeyErr = savedNoKey
		isParseErr = savedParse
	}()

	RegisterPlatformOutcomeHooks(
		func(err error) bool { return errors.Is(err, errTestNoKey) },
		func(err error) bool { return errors.Is(err, errTestParse) },
	)

	wrappedNoKey := fmt.Errorf("indeed: no api key: %w", errTestNoKey)
	wrappedParse := fmt.Errorf("habr career parse: %w: %w", errTestParse, errors.New("json decode"))

	cases := []struct {
		name string
		n    int
		err  error
		want string
	}{
		{"no_key direct", 0, errTestNoKey, "no_key"},
		{"no_key wrapped", 0, wrappedNoKey, "no_key"},
		{"parse_fail direct", 0, errTestParse, "parse_fail"},
		{"parse_fail wrapped", 0, wrappedParse, "parse_fail"},
		// timeout still wins over other hooks.
		{"timeout beats no_key", 0, errors.Join(context.DeadlineExceeded, errTestNoKey), "timeout"},
		// ok and empty unaffected.
		{"ok still ok", 2, nil, "ok"},
		{"empty still empty", 0, nil, "empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlatformOutcome(tc.n, tc.err)
			if got != tc.want {
				t.Errorf("PlatformOutcome(%d, %v) = %q, want %q", tc.n, tc.err, got, tc.want)
			}
		})
	}
}

// TestFF4_IncrPlatformResults_NewOutcomesAccepted verifies that the new outcome
// values pass IncrPlatformResults's bounded guard (i.e. they are not silently
// dropped by the validator).
func TestFF4_IncrPlatformResults_NewOutcomesAccepted(t *testing.T) {
	Init(Config{})
	newOutcomes := []string{"ok", "timeout", "no_key", "parse_fail"}
	platform := "greenhouse"

	for _, oc := range newOutcomes {
		before := GetMetrics()[MetricPlatformResults+"{platform="+platform+",outcome="+oc+"}"]
		IncrPlatformResults(platform, oc)
		after := GetMetrics()[MetricPlatformResults+"{platform="+platform+",outcome="+oc+"}"]
		if after-before != 1 {
			t.Errorf("outcome=%q not accepted by IncrPlatformResults (counter did not increment)", oc)
		}
	}
}

// TestFF4_IncrPlatformResults_OldResultsRejected verifies that the pre-ADR-J3
// "results" value is now REJECTED by the bounded-label guard (it was renamed to "ok").
// Revert-red: if validPlatformOutcomes is reverted to include "results", this test fails.
func TestFF4_IncrPlatformResults_OldResultsRejected(t *testing.T) {
	Init(Config{})
	platform := "greenhouse"
	before := GetMetrics()[MetricPlatformResults+"{platform="+platform+",outcome=results}"]
	IncrPlatformResults(platform, "results") // must be dropped
	after := GetMetrics()[MetricPlatformResults+"{platform="+platform+",outcome=results}"]
	if after != before {
		t.Errorf("old outcome 'results' should be rejected by IncrPlatformResults (counter should NOT increment)")
	}
}
