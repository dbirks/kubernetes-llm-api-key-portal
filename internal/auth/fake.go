package auth

import (
	"net/http"
)

// FakeUser is the identity granted by the development authenticator.
//
// The tenant and object IDs are obviously synthetic so that a key accidentally
// created under this identity is recognisable in a cluster.
var FakeUser = SessionUser{
	TenantID: "00000000-0000-0000-0000-00000000dev0",
	ObjectID: "00000000-0000-0000-0000-0000000000a1",
	Name:     "Dev User",
	Email:    "dev@localhost",
}

// FakeAuthenticator signs a fixed user in without contacting Entra.
//
// It exists so that UI work and handler tests do not need a tenant. Config
// enforces the guard rails that keep it out of production: it cannot be
// combined with the Kubernetes keystore, and it requires a loopback public URL.
// Every page it serves carries a development banner.
type FakeAuthenticator struct {
	sealer *Sealer
	user   SessionUser
}

// NewFakeAuthenticator returns a development authenticator for the given user.
// Passing a zero SessionUser uses FakeUser.
func NewFakeAuthenticator(sealer *Sealer, user SessionUser) *FakeAuthenticator {
	if user.TenantID == "" || user.ObjectID == "" {
		user = FakeUser
	}
	return &FakeAuthenticator{sealer: sealer, user: user}
}

// Start signs the fixed user in immediately and redirects to the callback,
// so the route shape matches the real flow.
func (f *FakeAuthenticator) Start(w http.ResponseWriter, r *http.Request, returnTo string) error {
	if err := f.sealer.Issue(w, f.user); err != nil {
		return err
	}
	if returnTo == "" {
		returnTo = "/account"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
	return nil
}

// Complete is unreachable in normal use, since Start never leaves the origin.
// It returns the fixed user so that a manually visited callback still works.
func (f *FakeAuthenticator) Complete(w http.ResponseWriter, r *http.Request) (SessionUser, string, error) {
	if err := f.sealer.Issue(w, f.user); err != nil {
		return SessionUser{}, "", err
	}
	return f.user, "/account", nil
}

var _ Provider = (*FakeAuthenticator)(nil)
