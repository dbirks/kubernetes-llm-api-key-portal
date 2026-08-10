package httpapp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
)

// page builds the view model every template embeds.
func (a *App) page(w http.ResponseWriter, r *http.Request, title string) Page {
	p := Page{
		Brand:     a.brand,
		Title:     title,
		RequestID: RequestIDFrom(r.Context()),
		DevMode:   a.devMode,
		Flashes:   a.sealer.TakeFlash(w, r),
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		p.User = &User{
			Name:     u.Name,
			Email:    u.Email,
			Initials: initials(u.Name, u.Email),
		}
	}
	return p
}

// owner extracts the authorization identity from the session. Handlers behind
// RequireUser can rely on ok being true.
func owner(r *http.Request) (keystore.Owner, bool) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		return keystore.Owner{}, false
	}
	return keystore.Owner{
		TenantID:    u.TenantID,
		ObjectID:    u.ObjectID,
		DisplayName: u.Name,
		Email:       u.Email,
	}, true
}

func (a *App) handleLanding(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); ok {
		http.Redirect(w, r, "/account", http.StatusFound)
		return
	}
	a.mustRender(w, r, http.StatusOK, "landing.html", LandingPage{
		Page: a.page(w, r, a.brand.Name),
	})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); ok {
		http.Redirect(w, r, "/account", http.StatusFound)
		return
	}
	// Only same-site relative paths are honoured as a return target, so this
	// cannot be used as an open redirect.
	returnTo := safeReturnPath(r.URL.Query().Get("next"))
	if err := a.auth.Start(w, r, returnTo); err != nil {
		a.log.Error("starting sign-in failed", "request_id", RequestIDFrom(r.Context()), "error", err)
		a.renderError(w, r, http.StatusInternalServerError,
			"We couldn't sign you in",
			"Try again, or contact the service owner if the problem continues.")
	}
}

func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	// The callback URL carries an authorization code, so nothing about this
	// response should be cached or referred onward.
	noStore(w)

	user, returnTo, err := a.auth.Complete(w, r)
	if err != nil {
		// The detail goes to the log; the user gets one message either way, so
		// a failed sign-in cannot be used to probe tenant membership.
		a.log.Warn("sign-in failed", "request_id", RequestIDFrom(r.Context()), "error", err)
		status := http.StatusUnauthorized
		message := "Try again, or contact the service owner if the problem continues."
		if errors.Is(err, auth.ErrWrongTenant) {
			status = http.StatusForbidden
			message = "That account isn't allowed to use this service. Sign in with your work account, or contact the service owner."
		}
		a.renderError(w, r, status, "We couldn't sign you in", message)
		return
	}

	a.log.Info("sign-in succeeded",
		"request_id", RequestIDFrom(r.Context()),
		"entra_tid", user.TenantID,
		"entra_oid", user.ObjectID)

	if returnTo == "" {
		returnTo = "/account"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.sealer.Clear(w)
	a.sealer.SetFlash(w, auth.FlashInfo, "You're signed out.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) handleAccount(w http.ResponseWriter, r *http.Request) {
	// The account page lists a user's keys, so it must not be cached by a
	// shared proxy or restored from the back/forward cache after sign-out.
	noStore(w)

	own, _ := owner(r)
	keys, err := a.store.ListKeys(r.Context(), own.ID())
	if err != nil {
		a.log.Error("listing keys failed",
			"request_id", RequestIDFrom(r.Context()),
			"entra_tid", own.TenantID, "entra_oid", own.ObjectID, "error", err)
		a.renderError(w, r, http.StatusInternalServerError,
			"We couldn't load your API keys",
			"Nothing was changed. Try again in a moment.")
		return
	}

	params := a.onboarding
	a.mustRender(w, r, http.StatusOK, "account.html", AccountPage{
		Page:   a.page(w, r, "Account"),
		Keys:   toKeyViews(keys),
		Guides: onboarding.Guides(params),
		EnvVar: onboarding.EnvVar(params),
	})
}

func (a *App) handleNewKey(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	a.mustRender(w, r, http.StatusOK, "key_new.html", NewKeyPage{
		Page: a.page(w, r, "Create API key"),
	})
}

func (a *App) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	// This response body contains the credential. It must never be stored by a
	// cache or restored from history.
	noStore(w)

	own, _ := owner(r)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, r, http.StatusBadRequest,
			"We couldn't read that form",
			"Nothing was changed. Go back and try again.")
		return
	}
	rawName := r.PostFormValue("name")

	name, err := keystore.ValidateName(rawName)
	if err != nil {
		// Re-render the form with the problem, keeping what the user typed.
		a.mustRender(w, r, http.StatusBadRequest, "key_new.html", NewKeyPage{
			Page:      a.page(w, r, "Create API key"),
			Name:      truncate(rawName, 200),
			NameError: userMessage(err),
		})
		return
	}

	created, err := a.store.CreateKey(r.Context(), own, name)
	if err != nil {
		a.log.Error("creating key failed",
			"request_id", RequestIDFrom(r.Context()),
			"entra_tid", own.TenantID, "entra_oid", own.ObjectID, "error", err)
		a.renderError(w, r, http.StatusInternalServerError,
			"We couldn't create your API key",
			"Nothing was changed. Try again.")
		return
	}

	// Note what is and is not logged: the resource name and owner, never the
	// credential or the user-supplied display name.
	a.log.Info("api key created",
		"request_id", RequestIDFrom(r.Context()),
		"entra_tid", own.TenantID, "entra_oid", own.ObjectID,
		"operation", "create", "key_resource_name", created.ID)

	// The credential is returned directly from the POST rather than through a
	// redirect. Redirecting would mean stashing the cleartext somewhere to
	// survive the round trip, which is worse than rendering it once here.
	params := a.onboarding
	params.APIKey = created.Secret
	a.mustRender(w, r, http.StatusOK, "key_created.html", CreatedKeyPage{
		Page:    a.page(w, r, "API key created"),
		KeyName: created.Name,
		Secret:  created.Secret,
		Guides:  onboarding.Guides(params),
		EnvVar:  onboarding.EnvVar(params),
	})
}

func (a *App) handleRevokeConfirm(w http.ResponseWriter, r *http.Request) {
	noStore(w)

	own, _ := owner(r)
	key, err := a.store.GetKey(r.Context(), own.ID(), r.PathValue("id"))
	if err != nil {
		a.renderKeyLookupError(w, r, own, err)
		return
	}
	a.mustRender(w, r, http.StatusOK, "key_revoke.html", RevokePage{
		Page: a.page(w, r, "Revoke API key"),
		Key:  toKeyView(key),
	})
}

func (a *App) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	own, _ := owner(r)
	id := r.PathValue("id")

	err := a.store.RevokeKey(r.Context(), own.ID(), id)
	if err != nil {
		a.renderKeyLookupError(w, r, own, err)
		return
	}

	a.log.Info("api key revoked",
		"request_id", RequestIDFrom(r.Context()),
		"entra_tid", own.TenantID, "entra_oid", own.ObjectID,
		"operation", "revoke", "key_resource_name", id)

	a.sealer.SetFlash(w, auth.FlashSuccess, "API key revoked. Anything using it will stop working.")
	// Post/Redirect/Get, so a refresh does not re-submit the revocation.
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// renderKeyLookupError handles the two outcomes of a key lookup. A key owned by
// someone else is indistinguishable from a missing one, by design.
func (a *App) renderKeyLookupError(w http.ResponseWriter, r *http.Request, own keystore.Owner, err error) {
	if errors.Is(err, keystore.ErrNotFound) {
		a.renderError(w, r, http.StatusNotFound,
			"That API key no longer exists",
			"It may already have been revoked.")
		return
	}
	a.log.Error("key lookup failed",
		"request_id", RequestIDFrom(r.Context()),
		"entra_tid", own.TenantID, "entra_oid", own.ObjectID, "error", err)
	a.renderError(w, r, http.StatusInternalServerError,
		"We couldn't reach your API keys",
		"Nothing was changed. Try again in a moment.")
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writePlain(w, http.StatusOK, "ok")
}

// handleReadyz reports whether the service can actually do its job. It checks
// the keystore, which in production means confirming the Kubernetes API is
// reachable and the ServiceAccount is authorised.
func (a *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ready(r.Context()); err != nil {
		// The detail stays in the log: a probe response is a poor place to
		// describe internal dependencies.
		a.log.Error("readiness check failed", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	writePlain(w, http.StatusOK, "ok")
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(body + "\n"))
}

func toKeyViews(keys []keystore.KeyMetadata) []KeyView {
	out := make([]KeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyView(k))
	}
	return out
}

func toKeyView(k keystore.KeyMetadata) KeyView {
	return KeyView{ID: k.ID, Name: k.Name, Suffix: k.Suffix, CreatedAt: k.CreatedAt}
}

// safeReturnPath accepts only same-site absolute paths, so a crafted ?next=
// cannot bounce a signed-in user to another origin.
func safeReturnPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	return u.Path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// userMessage strips the error-wrapping prefix so form errors read as guidance
// rather than as diagnostics.
func userMessage(err error) string {
	msg := err.Error()
	if _, rest, found := strings.Cut(msg, ": "); found {
		return strings.ToUpper(rest[:1]) + rest[1:]
	}
	return msg
}
