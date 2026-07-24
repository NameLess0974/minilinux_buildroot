package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// isDashboardPoll reports whether a request is a routine console read that the
// backend repeats every few seconds. These are logged only on error, to keep the
// log readable. Boot, images, ingestion and admin actions are always logged.
func isDashboardPoll(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch {
	case path == "/health",
		path == "/api/v1/fleet",
		path == "/api/v1/images",
		path == "/api/v1/sessions",
		strings.HasPrefix(path, "/api/v1/sessions/"):
		return true
	}
	return false
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	size        int64
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.size += int64(n)
	return n, err
}

// Flush implements http.Flusher
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging returns a middleware that logs HTTP requests
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := newResponseWriter(w)

			// Process request
			next.ServeHTTP(rw, r)

			// Log after request completes
			duration := time.Since(start)

			// Skip routine dashboard polling when it succeeded (still log its errors).
			if rw.status < 400 && isDashboardPoll(r.Method, r.URL.Path) {
				return
			}

			// Use different log levels based on status code
			if rw.status >= 500 {
				logger.Error("HTTP request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rw.status,
					"duration", duration,
					"size", rw.size,
					"remote", r.RemoteAddr)
			} else if rw.status >= 400 {
				logger.Warn("HTTP request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rw.status,
					"duration", duration,
					"size", rw.size,
					"remote", r.RemoteAddr)
			} else {
				logger.Info("HTTP request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rw.status,
					"duration", duration,
					"size", rw.size,
					"remote", r.RemoteAddr)
			}
		})
	}
}

// Recovery returns a middleware that recovers from panics
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					logger.Error("panic recovered",
						"error", err,
						"path", r.URL.Path,
						"method", r.Method,
						"stack", string(debug.Stack()))

					http.Error(w, "Internal server error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
