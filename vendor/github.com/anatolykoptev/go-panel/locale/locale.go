// Package locale provides the locale axis for go-panel i18n.
//
// Locale is ORTHOGONAL to tenant: tenant.Scope selects WHICH rows (e.g. city_slug);
// locale selects WHICH LANGUAGE of a row's translatable fields. They compose.
//
// Per go-panel's "app owns the schema" principle, this package owns the locale
// CONTRACT — the configured set, the active locale, and request resolution — NOT the
// storage of translations. The app's Lister/Writer persist and serve per-locale
// values; go-panel hands them the active locale via context.
//
// A deployment with no locale Middleware behaves as single-locale: From returns the
// empty Locale, which apps treat as "the default / untranslated value".
package locale

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Locale is a short language code, e.g. "en", "es", "ru". Empty = unset (default).
// Codes are compared exactly (case-sensitive) — use canonical lowercase; the
// caller (or frontend) normalizes incoming values before resolution.
type Locale string

// Set is a deployment's configured locales: an ordered Available list (display
// order) with a Default served at the bare path; others under their own prefix.
type Set struct {
	Default   Locale
	Available []Locale
}

// NewSet builds and validates a Set: non-empty default, default in available, no
// duplicates, no empty codes.
func NewSet(def Locale, available ...Locale) (Set, error) {
	if def == "" {
		return Set{}, errors.New("locale: default must not be empty")
	}
	seen := make(map[Locale]bool, len(available))
	foundDefault := false
	for _, l := range available {
		if l == "" {
			return Set{}, errors.New("locale: available contains an empty code")
		}
		if seen[l] {
			return Set{}, fmt.Errorf("locale: duplicate code %q", l)
		}
		seen[l] = true
		if l == def {
			foundDefault = true
		}
	}
	if !foundDefault {
		return Set{}, fmt.Errorf("locale: default %q not in available", def)
	}
	// Copy the slice so a caller mutating its backing array cannot corrupt this
	// validated, immutable Set (e.g. re-introduce a duplicate/empty code).
	return Set{Default: def, Available: append([]Locale(nil), available...)}, nil
}

// Has reports whether l is one of the configured locales.
func (s Set) Has(l Locale) bool {
	for _, a := range s.Available {
		if a == l {
			return true
		}
	}
	return false
}

// Resolve returns l when it is in the set, otherwise the Default. An empty or
// unknown l resolves to Default.
func (s Set) Resolve(l Locale) Locale {
	if s.Has(l) {
		return l
	}
	return s.Default
}

type ctxKey struct{}

// From returns the active Locale stored in ctx, or "" when none is set (treat as
// the default / untranslated value).
func From(ctx context.Context) Locale {
	if l, ok := ctx.Value(ctxKey{}).(Locale); ok {
		return l
	}
	return ""
}

// WithLocale stores l in ctx.
func WithLocale(ctx context.Context, l Locale) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Resolver resolves the active Locale from an HTTP request.
type Resolver interface {
	Resolve(r *http.Request) Locale
}

// QueryResolver resolves the locale from a query parameter (e.g. ?locale=es),
// validated against Set — an absent or unknown value resolves to Set.Default. This
// is the generic resolver for an app API or admin preview; the consumer frontend
// (Astro) owns path-prefix (/es/) routing.
type QueryResolver struct {
	Param string // query param name; defaults to "locale" when empty
	Set   Set
}

// Resolve implements Resolver.
func (qr QueryResolver) Resolve(r *http.Request) Locale {
	param := qr.Param
	if param == "" {
		param = "locale"
	}
	return qr.Set.Resolve(Locale(r.URL.Query().Get(param)))
}

// Middleware resolves the active Locale from r and stores it in the request
// context. Mount it where locale-aware handlers run.
func Middleware(resolver Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithLocale(r.Context(), resolver.Resolve(r))))
	})
}
