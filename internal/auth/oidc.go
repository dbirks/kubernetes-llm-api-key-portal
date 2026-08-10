// Package auth handles who the user is: the Entra OIDC sign-in flow, the
// sealed session cookie that follows it, and the middleware that gates
// protected routes.
//
// Token verification, discovery, and JWKS refresh are delegated to go-oidc.
// This package owns the parts that are application policy: which tenant is
// accepted, what goes in the session, and how state and nonce are carried.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// authFlowTTL bounds how long a sign-in may take between /login and the
// callback. Long enough for a real MFA prompt, short enough that abandoned
// flows do not linger.
const authFlowTTL = 15 * time.Minute

// Errors surfaced to the callback handler. They are deliberately coarse: the
// user sees one message regardless, and the detail goes to the log.
var (
	ErrAuthFailed  = errors.New("authentication failed")
	ErrWrongTenant = errors.New("account is not in an accepted tenant")
)

// Authenticator drives the OIDC authorization-code flow.
type Authenticator struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	sealer   *Sealer

	// acceptedTenants is the set of Entra tenant IDs allowed to sign in.
	//
	// v1 always populates this with exactly one tenant. It is a set rather than
	// a scalar because the documented future direction is a multi-tenant app
	// registration with an explicit allowlist, and that change should not have
	// to reach into the verification logic.
	acceptedTenants map[string]bool
}

// AuthenticatorConfig is the input to NewAuthenticator.
type AuthenticatorConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Tenants      []string
}

// NewAuthenticator performs OIDC discovery against the configured issuer.
//
// Discovery is a network call, so this is done once at startup and a failure
// here prevents the process from coming up.
func NewAuthenticator(ctx context.Context, cfg AuthenticatorConfig, sealer *Sealer) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.IssuerURL, err)
	}
	accepted := make(map[string]bool, len(cfg.Tenants))
	for _, t := range cfg.Tenants {
		if t != "" {
			accepted[t] = true
		}
	}
	if len(accepted) == 0 {
		return nil, errors.New("at least one accepted tenant is required")
	}

	return &Authenticator{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			// Authentication only. The portal has no use for Microsoft Graph,
			// so it asks for no Graph scopes and needs no admin consent beyond
			// basic sign-in.
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		// go-oidc checks the signature, issuer, audience, and expiry.
		verifier:        provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		sealer:          sealer,
		acceptedTenants: accepted,
	}, nil
}

// authFlow is the short-lived state carried across the redirect to Microsoft.
type authFlow struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Return   string    `json:"r,omitempty"`
	IssuedAt time.Time `json:"t"`
}

// Start begins a sign-in: it stores state and nonce in a sealed cookie and
// redirects to Microsoft.
//
// Keeping state in an encrypted cookie rather than server memory means the
// portal stays stateless and horizontally scalable, and it survives a restart
// mid-login.
func (a *Authenticator) Start(w http.ResponseWriter, r *http.Request, returnTo string) error {
	state, err := randomToken()
	if err != nil {
		return err
	}
	nonce, err := randomToken()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(authFlow{
		State:    state,
		Nonce:    nonce,
		Return:   returnTo,
		IssuedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("encode auth flow: %w", err)
	}
	sealed, err := a.sealer.seal(payload)
	if err != nil {
		return err
	}
	http.SetCookie(w, a.sealer.cookie(
		a.sealer.name(oauthStateCookieName, oauthStateCookieNameDev), sealed, authFlowTTL))

	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
	return nil
}

// Complete finishes a sign-in and returns the authenticated user.
//
// The returned string is where the user should be sent next.
func (a *Authenticator) Complete(w http.ResponseWriter, r *http.Request) (SessionUser, string, error) {
	cookieName := a.sealer.name(oauthStateCookieName, oauthStateCookieNameDev)
	// The flow cookie is single-use regardless of outcome.
	defer a.sealer.expire(w, cookieName)

	c, err := r.Cookie(cookieName)
	if err != nil {
		return SessionUser{}, "", fmt.Errorf("%w: no in-progress sign-in", ErrAuthFailed)
	}
	payload, err := a.sealer.open(c.Value)
	if err != nil {
		return SessionUser{}, "", fmt.Errorf("%w: unreadable sign-in state", ErrAuthFailed)
	}
	var flow authFlow
	if err := json.Unmarshal(payload, &flow); err != nil {
		return SessionUser{}, "", fmt.Errorf("%w: malformed sign-in state", ErrAuthFailed)
	}
	if time.Since(flow.IssuedAt) > authFlowTTL {
		return SessionUser{}, "", fmt.Errorf("%w: sign-in took too long", ErrAuthFailed)
	}

	// Microsoft may report a user-facing failure (consent declined, for
	// instance) instead of returning a code.
	if errCode := r.URL.Query().Get("error"); errCode != "" {
		return SessionUser{}, "", fmt.Errorf("%w: provider returned %q", ErrAuthFailed, errCode)
	}

	// Compare in constant time; state is a secret-equivalent value.
	if !constantTimeEqual(r.URL.Query().Get("state"), flow.State) {
		return SessionUser{}, "", fmt.Errorf("%w: state mismatch", ErrAuthFailed)
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return SessionUser{}, "", fmt.Errorf("%w: no authorization code", ErrAuthFailed)
	}

	token, err := a.oauth.Exchange(r.Context(), code)
	if err != nil {
		// The underlying error can quote the authorization code, so it is
		// summarised rather than wrapped.
		return SessionUser{}, "", fmt.Errorf("%w: code exchange rejected", ErrAuthFailed)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		return SessionUser{}, "", fmt.Errorf("%w: no id_token in response", ErrAuthFailed)
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		return SessionUser{}, "", fmt.Errorf("%w: id_token verification failed", ErrAuthFailed)
	}
	if !constantTimeEqual(idToken.Nonce, flow.Nonce) {
		return SessionUser{}, "", fmt.Errorf("%w: nonce mismatch", ErrAuthFailed)
	}

	var claims struct {
		TenantID          string `json:"tid"`
		ObjectID          string `json:"oid"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return SessionUser{}, "", fmt.Errorf("%w: unreadable claims", ErrAuthFailed)
	}
	// tid and oid together are the only identity we trust. Without both there
	// is nothing stable to own a key.
	if claims.TenantID == "" || claims.ObjectID == "" {
		return SessionUser{}, "", fmt.Errorf("%w: token is missing tid or oid", ErrAuthFailed)
	}
	if !a.acceptedTenants[claims.TenantID] {
		return SessionUser{}, "", ErrWrongTenant
	}

	// email is optional; its absence is not an authentication failure. Fall
	// back to preferred_username purely for display.
	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}

	user := SessionUser{
		TenantID: claims.TenantID,
		ObjectID: claims.ObjectID,
		Name:     claims.Name,
		Email:    email,
	}
	if err := a.sealer.Issue(w, user); err != nil {
		return SessionUser{}, "", err
	}
	return user, flow.Return, nil
}
