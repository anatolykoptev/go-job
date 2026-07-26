package engine

import (
	"regexp"
	"testing"

	stealth "github.com/anatolykoptev/go-stealth"
)

var uaChromeMajorRE = regexp.MustCompile(`Chrome/(\d+)`)

// uaChromeMajor extracts the Chrome major version token from a User-Agent
// string (e.g. "Chrome/131.0.0.0" -> "131"). Fatals if absent — every UA in
// this invariant's domain is Chromium-based by construction.
func uaChromeMajor(t *testing.T, ua string) string {
	t.Helper()
	m := uaChromeMajorRE.FindStringSubmatch(ua)
	if m == nil {
		t.Fatalf("no Chrome major version token in UA %q", ua)
	}
	return m[1]
}

// TestUserAgentChrome_MatchesDefaultProfile pins the invariant this refactor
// establishes: the User-Agent go-job sends via UserAgentChrome must be derived
// from the fleet's stealth TLS profile (ProfileChrome131 — the profile
// stealth.defaultConfig installs when no WithProfile is given, which is how
// go-engine's fetch.New builds the repo's BrowserClient), not a hardcoded
// literal that can drift from the presenting profile's Chrome major.
//
// go-stealth pairs a UA with a JA3 fingerprint as a matched pair in
// BuiltinProfiles. A hardcoded UA literal riding a different profile (or a
// different OS variant of the same profile) is a self-inconsistent pair: no
// real Chrome on one OS produces another OS's handshake, and a Chrome N UA
// over a Chrome M JA3 (N != M) is a stronger bot signal than being merely
// out of date. Deriving UserAgentChrome from stealth.UserAgentForProfile makes
// the UA agree with the fleet's canonical identity by contract.
//
// RED against the pre-refactor hardcoded literal: UserAgentChrome was a Linux
// Chrome/131 string while UserAgentForProfile(ProfileChrome131) resolves the
// Windows variant (the first BuiltinProfiles entry) — the exact-string
// assertion failed, proving the literal was not derived from the profile.
func TestUserAgentChrome_MatchesDefaultProfile(t *testing.T) {
	t.Parallel()

	wantUA := stealth.UserAgentForProfile(stealth.ProfileChrome131)
	if wantUA == "" {
		t.Fatal("stealth.UserAgentForProfile(ProfileChrome131) returned empty — accessor missing or profile unknown")
	}
	wantMajor := uaChromeMajor(t, wantUA)

	// Exact-string: UserAgentChrome must BE the profile-derived UA, not a
	// hardcoded literal that happens to share a Chrome major.
	if UserAgentChrome != wantUA {
		t.Errorf("UserAgentChrome = %q\nwant (profile-derived) = %q\n"+
			"UserAgentChrome must equal stealth.UserAgentForProfile(ProfileChrome131), not a hardcoded literal",
			UserAgentChrome, wantUA)
	}

	// Chrome-major invariant: even if the profile is bumped, the UA's Chrome
	// major must follow it.
	gotMajor := uaChromeMajor(t, UserAgentChrome)
	if gotMajor != wantMajor {
		t.Errorf("UserAgentChrome Chrome major = %q, want %q (must match ProfileChrome131; UA=%q want=%q)",
			gotMajor, wantMajor, UserAgentChrome, wantUA)
	}

	// The fleet's *stealth.BrowserClient (built with no WithProfile → default
	// ProfileChrome131) must present the same UA via Identity(). This is the
	// matched-pair guarantee: the UA go-job sends agrees with the JA3 the
	// stealth client presents.
	bc, err := stealth.NewClient()
	if err != nil {
		t.Fatalf("stealth.NewClient: %v", err)
	}
	idUA := bc.Identity().UserAgent
	if idUA != wantUA {
		t.Errorf("BrowserClient.Identity().UserAgent = %q, want %q (must equal UserAgentForProfile(ProfileChrome131))",
			idUA, wantUA)
	}
}
