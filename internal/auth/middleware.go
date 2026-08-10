package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
)

type contextKey struct{ name string }

var userContextKey = contextKey{"session-user"}

// WithUser stores an authenticated user on the request context.
func WithUser(ctx context.Context, user SessionUser) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// UserFrom returns the authenticated user, if any.
func UserFrom(ctx context.Context) (SessionUser, bool) {
	user, ok := ctx.Value(userContextKey).(SessionUser)
	return user, ok
}

// Provider is what handlers need from authentication. The real Authenticator
// and the development stand-in both satisfy it, which is what lets handler
// tests run without Entra.
type Provider interface {
	// Start begins a sign-in, redirecting as needed.
	Start(w http.ResponseWriter, r *http.Request, returnTo string) error
	// Complete finishes a sign-in, returning the user and where to send them.
	Complete(w http.ResponseWriter, r *http.Request) (SessionUser, string, error)
}

var _ Provider = (*Authenticator)(nil)

// LoadSession attaches the session user to the request context when one is
// present. It never rejects a request; gating is RequireUser's job.
func LoadSession(sealer *Sealer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, err := sealer.Read(r); err == nil {
				r = r.WithContext(WithUser(r.Context(), user))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireUser gates a handler behind a valid session.
//
// Unauthenticated GETs are redirected to sign in; anything else gets a plain
// 401, since redirecting a form POST to a login page would silently discard the
// submission.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		http.Error(w, "Sign in to continue.", http.StatusUnauthorized)
	})
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func constantTimeEqual(a, b string) bool {
	// Compare lengths first only via subtle, so equal-length values do not leak
	// timing. An unequal length is not secret.
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
