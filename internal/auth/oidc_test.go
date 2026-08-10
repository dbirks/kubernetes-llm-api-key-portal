package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// discoveryStub serves just enough of an OIDC provider for NewAuthenticator to
// complete discovery. No token is ever exchanged against it.
func discoveryStub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL,
			"authorization_endpoint":                server.URL + "/authorize",
			"token_endpoint":                        server.URL + "/token",
			"jwks_uri":                              server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	return server.URL
}

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	sealer, err := NewSealer([][]byte{bytes.Repeat([]byte{3}, 32)}, false)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return sealer
}

// requestedScopes drives a sign-in and reports what the authorization redirect
// actually asked Microsoft for.
func requestedScopes(t *testing.T, photos PhotoCapturer) []string {
	t.Helper()
	a, err := NewAuthenticator(context.Background(), AuthenticatorConfig{
		IssuerURL:    discoveryStub(t),
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://portal.example/auth/callback",
		Tenants:      []string{"tenant-id"},
		Photos:       photos,
	}, testSealer(t))
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := a.Start(rec, httptest.NewRequest(http.MethodGet, "/login", nil), ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	return strings.Fields(location.Query().Get("scope"))
}

// capturerStub is a do-nothing PhotoCapturer. Its only job is to be non-nil,
// which is how the authenticator is told avatars are enabled.
type capturerStub struct{}

func (capturerStub) Capture(ctx context.Context, key, accessToken string) {}

// With avatars off the portal must ask for nothing beyond sign-in. This is the
// promise the deployment notes make about Graph permissions.
func TestNoGraphScopeWithoutPhotos(t *testing.T) {
	scopes := requestedScopes(t, nil)

	for _, scope := range scopes {
		if strings.Contains(scope, "graph.microsoft.com") || strings.Contains(scope, "User.Read") {
			t.Errorf("requested %q with photos disabled; scopes were %v", scope, scopes)
		}
	}
	for _, want := range []string{"openid", "profile", "email"} {
		if !slices.Contains(scopes, want) {
			t.Errorf("scopes %v are missing %q", scopes, want)
		}
	}
}

func TestGraphScopeIsRequestedForPhotos(t *testing.T) {
	scopes := requestedScopes(t, capturerStub{})

	if !slices.Contains(scopes, graphPhotoScope) {
		t.Errorf("scopes %v do not include %q", scopes, graphPhotoScope)
	}
	// Still exactly one Graph permission: reading your own profile, nothing
	// tenant-wide.
	for _, scope := range scopes {
		if strings.Contains(scope, "graph.microsoft.com") && scope != graphPhotoScope {
			t.Errorf("requested an extra Graph scope %q", scope)
		}
	}
	// The OIDC scopes must survive the addition; dropping them would break the
	// id_token the whole session depends on.
	for _, want := range []string{"openid", "profile", "email"} {
		if !slices.Contains(scopes, want) {
			t.Errorf("scopes %v are missing %q", scopes, want)
		}
	}
}

func TestPhotoKeyIsTheAuthorizationTuple(t *testing.T) {
	u := SessionUser{TenantID: "tid", ObjectID: "oid"}
	if got := u.PhotoKey(); got != "tid:oid" {
		t.Errorf("PhotoKey() = %q, want tid:oid", got)
	}
	// An incomplete identity must not produce a key that could collide with
	// another partial one.
	for _, u := range []SessionUser{{TenantID: "tid"}, {ObjectID: "oid"}, {}} {
		if got := u.PhotoKey(); got != "" {
			t.Errorf("PhotoKey() = %q for %+v, want empty", got, u)
		}
	}
}
