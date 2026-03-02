package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	requestIDHeader = "X-Request-ID"
	idBytes         = 16
)

// contextKey is an unexported type for context keys in this package.
type contextKey int

const requestIDKey contextKey = 0

// Middleware is an HTTP middleware that wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware in order: the first argument becomes the outermost
// wrapper, so requests pass through mw[0] → mw[1] → … → handler.
func Chain(handler http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		handler = mw[i](handler)
	}
	return handler
}

// Recovery returns middleware that recovers from panics and returns 500.
func Recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					logger.Error("panic recovered",
						slog.Any("panic", rv),
						slog.String("path", r.URL.Path),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID returns middleware that generates or propagates X-Request-ID.
// If the incoming request has the header, it is reused; otherwise a random
// hex ID is generated. The value is stored in the request context.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if id == "" {
				id = generateID()
			}
			w.Header().Set(requestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext retrieves the request ID stored by [RequestID] middleware.
// Returns an empty string if no ID is present.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// RequestLog returns middleware that logs method, path, status, duration,
// and request_id for every request.
func RequestLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			logger.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFromContext(r.Context())),
			)
		})
	}
}

const defaultAllowHeaders = "Content-Type, Authorization, X-Request-ID"

// CORSConfig controls CORS middleware behavior.
type CORSConfig struct {
	Origins      []string // required; ["*"] = allow all
	MaxAge       int      // preflight Max-Age in seconds; 0 = omit
	AllowHeaders []string // nil = default (Content-Type, Authorization, X-Request-ID)
}

// CORS returns middleware that handles Cross-Origin Resource Sharing.
// Pass ["*"] in Origins to allow all origins. Preflight OPTIONS requests get a 204.
func CORS(cfg CORSConfig) Middleware {
	allow := make(map[string]struct{}, len(cfg.Origins))
	wildcard := false
	for _, o := range cfg.Origins {
		if o == "*" {
			wildcard = true
		}
		allow[o] = struct{}{}
	}
	headers := defaultAllowHeaders
	if len(cfg.AllowHeaders) > 0 {
		headers = strings.Join(cfg.AllowHeaders, ", ")
	}
	maxAge := ""
	if cfg.MaxAge > 0 {
		maxAge = strconv.Itoa(cfg.MaxAge)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !matchOrigin(origin, wildcard, allow) {
				next.ServeHTTP(w, r)
				return
			}
			setCORSHeaders(w, origin, wildcard, headers, maxAge)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchOrigin(origin string, wildcard bool, allow map[string]struct{}) bool {
	if wildcard {
		return true
	}
	_, ok := allow[origin]
	return ok
}

func setCORSHeaders(w http.ResponseWriter, origin string, wildcard bool, headers, maxAge string) {
	if wildcard {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", headers)
	if maxAge != "" {
		w.Header().Set("Access-Control-Max-Age", maxAge)
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status = code
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying http.ResponseWriter for http.ResponseController.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
func generateID() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
