package auth

import "time"

// Op identifies which auth-package handler produced an observation.
//
// auth deliberately does NOT import identity (identity root -> identity/session
// -> go-redis, which would drag go-redis into every bcrypt-only consumer), so
// this is a local, dependency-light enum rather than a re-export of
// identity.Op. A single identity/promobs.Observer adapts both seams onto the
// same Prometheus metric family — see promobs.Observer.AsAuthObserver.
type Op uint8

const (
	// OpSessionRecheck is the per-request liveSession revocation recheck
	// (Require -> liveSession -> AccountStore.GetByID).
	OpSessionRecheck Op = iota + 1
	// OpBcryptLogin is the LoginHandler POST branch: checkRateLimit,
	// verifyPassword, and issueSession each observe under this Op. Emitted
	// since Phase 2 (login-outcome instrumentation).
	OpBcryptLogin
)

// Outcome classifies the terminal result of an auth-package operation.
type Outcome uint8

const (
	// OutcomeOK means the operation completed successfully. For
	// OpBcryptLogin this means issueSession minted and set the session
	// cookie. OpSessionRecheck never emits OutcomeOK — only its transient-
	// error degrade is observed (see OutcomeError).
	OutcomeOK Outcome = iota + 1
	// OutcomeInvalidCredentials means a login attempt or a session's live
	// revocation check was denied for a reason the caller controls (unknown
	// email, wrong password, a revoked/deactivated account, role drift) — as
	// opposed to an infrastructure failure (OutcomeError). Emitted by
	// LoginHandler's verifyPassword step (OpBcryptLogin) since Phase 2.
	// liveSession's revocation-deny branches (ErrAccountNotFound, !Active,
	// role change) still return nil without observing — unchanged from
	// Phase 1, out of this phase's scope.
	OutcomeInvalidCredentials
	// OutcomeError means an infrastructure failure occurred: for
	// OpSessionRecheck, the AccountStore returned a transient, non-not-found
	// error; for OpBcryptLogin, issueSession's makeToken (crypto/rand) call
	// failed. Both are terminal branches with no caller-controlled cause, as
	// opposed to OutcomeInvalidCredentials.
	OutcomeError
	// OutcomeRateLimited means the login attempt was rejected by
	// BcryptConfig.RateLimiter as over-quota (Allow returned false). Emitted
	// by checkRateLimit (OpBcryptLogin) since Phase 2.
	OutcomeRateLimited
	// OutcomeLimiterError means the RateLimiter itself failed (e.g. a Redis
	// outage) — distinct from OutcomeRateLimited, which means the limiter
	// answered and denied the attempt. Both are treated fail-closed (429 +
	// Retry-After, bcrypt never reached). Emitted by checkRateLimit
	// (OpBcryptLogin) since Phase 2.
	OutcomeLimiterError
)

// Observer receives a single observation per completed auth-package
// operation. Implementations must be concurrency-safe — Observe is called
// from arbitrary request-handling goroutines.
//
// Typical use: a host wires identity/promobs.Observer (via
// AsAuthObserver) into BcryptConfig.Observer so one concrete Prometheus
// observer serves both the identity and auth seams.
type Observer interface {
	// Observe is called exactly once per observed operation, after the
	// terminal outcome is known. op and outcome identify the operation and
	// its result; dur is the wall-clock time spent on that operation.
	Observe(op Op, outcome Outcome, dur time.Duration)
}

// NopObserver is a no-op [Observer] used as the default when
// BcryptConfig.Observer is nil. It satisfies the interface without allocating.
type NopObserver struct{}

// Observe discards all observations.
func (NopObserver) Observe(Op, Outcome, time.Duration) {}
