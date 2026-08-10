package httpapp

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type ctxKey struct{ name string }

var requestIDKey = ctxKey{"request-id"}

// RequestIDFrom returns the current request's correlation ID.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withRequestID assigns each request a short correlation ID.
//
// It is generated here rather than taken from an inbound header: the service
// sits behind a tunnel and a gateway, and accepting a client-supplied ID would
// let a caller forge log correlation or inject arbitrary text into log fields.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			// A request without an ID is still serviceable; correlation just
			// degrades.
			next.ServeHTTP(w, r)
			return
		}
		id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests emits one structured record per request.
//
// Fields are listed explicitly rather than reflected from the request, so a
// header or query string carrying a credential cannot reach the log by
// accident. Note the absence of the raw URL: only the matched route pattern and
// the path are recorded, never the query string.
//
// The pattern is resolved up front with mux.Handler rather than read from
// r.Pattern afterwards. ServeMux sets Pattern on the copy of the request it
// hands to the handler, so a middleware wrapped around the mux never observes
// it, which silently blanks the field for every route with a wildcard.
func logRequests(log *slog.Logger, mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			_, pattern := mux.Handler(r)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			attrs := []any{
				"request_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"route", pattern,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			}
			log.LogAttrs(r.Context(), levelForStatus(rec.status), "http request", toAttrs(attrs)...)
		})
	}
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func toAttrs(kv []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		attrs = append(attrs, slog.Any(key, kv[i+1]))
	}
	return attrs
}

// recoverPanics turns a handler panic into a 500 rather than a dropped
// connection, and logs it with the request ID so it can be found.
func (a *App) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.log.Error("panic serving request",
					"request_id", RequestIDFrom(r.Context()),
					"path", r.URL.Path,
					"panic", fmt.Sprint(rec))
				a.renderError(w, r, http.StatusInternalServerError,
					"Something went wrong",
					"Nothing changed. Try again. If it keeps happening, ask the service owner.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is the site-wide CSP.
//
// The site is entirely first-party, which is what lets this stay strict:
// no inline script or style, no third-party origins, and no framing. The one
// concession is form-action, which must permit the Microsoft login origin
// because the sign-in redirect is a form POST target in some flows.
func (a *App) contentSecurityPolicy() string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"object-src 'none'",
		"form-action 'self' https://login.microsoftonline.com",
	}, "; ")
}

// securityHeaders applies the headers every response should carry.
func (a *App) securityHeaders(next http.Handler) http.Handler {
	csp := a.contentSecurityPolicy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if a.secureCookies {
			// Only meaningful over HTTPS, and actively unhelpful on loopback.
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// noStore marks a response as uncacheable. Used for anything that renders a
// credential or depends on the current session.
func noStore(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
}

// chain applies middleware so that the first listed runs outermost.
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
