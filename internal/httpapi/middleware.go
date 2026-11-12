package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/reqctx"
)

const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://login.microsoftonline.com"

// devCSP allows the Vite dev server's inline preamble and HMR websocket.
const devCSP = csp + "; script-src 'self' 'unsafe-inline'; connect-src 'self' ws:"

func securityHeaders(hsts, dev bool, next http.Handler) http.Handler {
	policy := csp
	if dev {
		policy = devCSP
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// routeHolder lets the innermost handler report its pattern to the outer
// logging/metrics middleware (nested muxes and r.WithContext copies hide
// r.Pattern from outer layers).
type routeHolder struct{ pattern string }

type routeKey struct{}

func setRoute(ctx context.Context, pattern string) {
	if h, ok := ctx.Value(routeKey{}).(*routeHolder); ok {
		h.pattern = pattern
	}
}

// route wraps h so the route pattern (never the raw path) is used as label.
func route(pattern string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRoute(r.Context(), pattern)
		h.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// observe logs every request and records metrics; it also recovers panics.
func observe(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		holder := &routeHolder{pattern: "unmatched"}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, holder))
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error("panic", "route", holder.pattern, "panic", rec, "stack", string(debug.Stack()))
				if !sw.wrote {
					httpx.WriteError(sw, httpx.ErrInternal)
				}
			}
			d := time.Since(start)
			metrics.HTTPRequests.WithLabelValues(holder.pattern, r.Method, strconv.Itoa(sw.status)).Inc()
			metrics.HTTPDuration.WithLabelValues(holder.pattern).Observe(d.Seconds())
			if holder.pattern == "GET /healthz" || holder.pattern == "GET /readyz" {
				return
			}
			attrs := []any{"method", r.Method, "route", holder.pattern, "status", sw.status, "duration_ms", d.Milliseconds()}
			if u := userOf(r); u != "" {
				attrs = append(attrs, "user_id", u)
			}
			log.Info("http", attrs...)
		}()
		next.ServeHTTP(sw, r)
	})
}

// userOf reads the user id that LoadSession stored. LoadSession runs inside
// observe, so it publishes the id through the holder in the context.
func userOf(r *http.Request) string {
	if h, ok := r.Context().Value(userHolderKey{}).(*userHolder); ok {
		return h.id
	}
	return ""
}

type userHolder struct{ id string }
type userHolderKey struct{}

func withUserHolder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := &userHolder{}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userHolderKey{}, h)))
	})
}

// publishUser copies the authenticated user id into the holder for logging.
func publishUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := r.Context().Value(userHolderKey{}).(*userHolder); ok {
			if u := reqctx.UserFrom(r.Context()); u != nil {
				h.id = u.ID
			}
		}
		next.ServeHTTP(w, r)
	})
}

func limitBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			httpx.WriteError(w, httpx.ErrTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}
