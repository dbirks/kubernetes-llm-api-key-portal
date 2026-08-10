package httpapp

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/brand"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/config"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore/memory"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
	"github.com/dbirks/kubernetes-llm-api-key-portal/web"
)

// testHarness is an assembled application plus the pieces a test needs to poke
// at it: a fake sign-in, an in-memory store, and captured logs.
type testHarness struct {
	app    *App
	store  *memory.Store
	sealer *auth.Sealer
	logs   *bytes.Buffer
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	brandData, err := brand.Resolve(config.BrandConfig{
		Name:      "llm.birks.dev",
		ShortName: "llm.birks.dev",
		Tagline:   "Private self-hosted AI endpoint",
		LogoAlt:   "llm.birks.dev",
		Accent:    "#3b6fd6",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("brand.Resolve: %v", err)
	}

	// Secure cookies are off so the harness matches a loopback deployment and
	// httptest's plain-http requests carry the cookies back.
	sealer, err := auth.NewSealer([][]byte{bytes.Repeat([]byte{7}, 32)}, false)
	if err != nil {
		t.Fatalf("auth.NewSealer: %v", err)
	}

	store := memory.New("llm_")
	app, err := New(Options{
		Log:    log,
		Brand:  brandData,
		Store:  store,
		Sealer: sealer,
		Auth:   auth.NewFakeAuthenticator(sealer, auth.FakeUser),
		Assets: web.Assets(),
		Onboarding: onboarding.Params{
			BaseURL:   "https://llm.birks.dev",
			Model:     "Qwen3-Coder-30B",
			BrandName: "llm.birks.dev",
		},
	})
	if err != nil {
		t.Fatalf("httpapp.New: %v", err)
	}
	return &testHarness{app: app, store: store, sealer: sealer, logs: &logs}
}

// signIn returns a cookie representing an authenticated session.
func (h *testHarness) signIn(t *testing.T, user auth.SessionUser) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := h.sealer.Issue(rec, user); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("Issue set no cookie")
	}
	return cookies[0]
}

// do performs a request, optionally authenticated.
func (h *testHarness) do(t *testing.T, req *http.Request, session *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	h.app.ServeHTTP(rec, req)
	return rec
}

// postForm builds a same-origin form submission. Sec-Fetch-Site marks it as
// same-origin, which is what the stdlib cross-origin protection checks.
func postForm(target string, values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}

func testOwner(user auth.SessionUser) keystore.Owner {
	return keystore.Owner{
		TenantID: user.TenantID, ObjectID: user.ObjectID,
		DisplayName: user.Name, Email: user.Email,
	}
}

func TestHealthEndpoints(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, path, nil), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "ok" {
			t.Errorf("GET %s body = %q, want ok", path, body)
		}
		// Probes must not leak anything about internal dependencies.
		if strings.Contains(rec.Body.String(), "kubernetes") {
			t.Errorf("GET %s leaks dependency detail", path)
		}
	}
}

func TestLandingPage(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/", nil), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in with Microsoft") {
		t.Error("landing page is missing the Microsoft sign-in button")
	}
	if !strings.Contains(body, "llm.birks.dev") {
		t.Error("landing page does not show the configured brand name")
	}
}

func TestLandingRedirectsWhenSignedIn(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/", nil), h.signIn(t, auth.FakeUser))

	if rec.Code != http.StatusFound {
		t.Fatalf("GET / signed in = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/account" {
		t.Errorf("Location = %q, want /account", got)
	}
}

// The explainer is static content, so it must render for a visitor deciding
// whether to sign in as well as for someone already signed in.
func TestHowItWorksIsPublic(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name    string
		session *http.Cookie
	}{
		{name: "signed out"},
		{name: "signed in", session: h.signIn(t, auth.FakeUser)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, httptest.NewRequest(http.MethodGet, "/how-it-works", nil), tc.session)
			if rec.Code != http.StatusOK {
				t.Errorf("GET /how-it-works %s = %d, want 200", tc.name, rec.Code)
			}
		})
	}
}

func TestProtectedRoutesRequireASession(t *testing.T) {
	h := newHarness(t)

	// A GET is redirected to sign in.
	for _, path := range []string{"/account", "/keys/new", "/keys/abc/revoke"} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, path, nil), nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET %s = %d %q, want a redirect to /login", path, rec.Code, rec.Header().Get("Location"))
		}
	}

	// A POST is rejected outright: redirecting would silently drop the
	// submission.
	rec := h.do(t, postForm("/keys", url.Values{"name": {"x"}}), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /keys unauthenticated = %d, want 401", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/", nil), nil)

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		// The sign-in flow posts to Microsoft, so that origin must be allowed.
		"form-action 'self' https://login.microsoftonline.com",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP is missing %q; got %q", want, csp)
		}
	}
	// No unsafe escape hatches: the site has no inline script or style.
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP contains %q", forbidden)
		}
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestCreateKeyShowsCredentialOnce(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	rec := h.do(t, postForm("/keys", url.Values{"name": {"MacBook Claude Code"}}), session)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /keys = %d, want 200\n%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "MacBook Claude Code") {
		t.Error("the one-time page does not show the key name")
	}

	// A credential must never be cached or restored from history.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}

	secret := extractSecret(t, body)

	// The credential belongs in the document body and nowhere else.
	if strings.Contains(rec.Header().Get("Location"), secret) {
		t.Error("credential appears in a redirect")
	}
	for _, c := range rec.Result().Cookies() {
		if strings.Contains(c.Value, secret) {
			t.Errorf("credential appears in cookie %s", c.Name)
		}
	}
	if strings.Contains(body, `id="`+secret) || strings.Contains(body, "data-copy-target=\""+secret) {
		t.Error("credential appears in an element attribute")
	}

	// And the account page must never show it again.
	accountRec := h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), session)
	if strings.Contains(accountRec.Body.String(), secret) {
		t.Error("the account listing exposes the credential")
	}
}

// Nothing on the create path may write the credential to the log.
func TestCredentialIsNeverLogged(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	rec := h.do(t, postForm("/keys", url.Values{"name": {"MacBook"}}), session)
	secret := extractSecret(t, rec.Body.String())

	h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), session)

	logs := h.logs.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("the credential was written to the log:\n%s", logs)
	}
	// The trailing portion is enough to identify the key; the whole credential
	// must not appear even in fragments.
	if body := strings.TrimPrefix(secret, "llm_"); strings.Contains(logs, body) {
		t.Fatal("the credential body was written to the log")
	}
	// The create should still have produced an audit record.
	if !strings.Contains(logs, "api key created") {
		t.Error("no audit record was logged for the create")
	}
}

func TestCreateKeyRejectsABlankName(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	rec := h.do(t, postForm("/keys", url.Values{"name": {"   "}}), session)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /keys with a blank name = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "name") {
		t.Error("the re-rendered form does not explain the problem")
	}

	keys, err := h.store.ListKeys(t.Context(), testOwner(auth.FakeUser).ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("a key was created despite the invalid name: %+v", keys)
	}
}

// The key name is user-controlled and must be escaped, not interpreted.
func TestKeyNameIsEscapedInHTML(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	const payload = `<script>alert('xss')</script>`
	if _, err := h.store.CreateKey(t.Context(), testOwner(auth.FakeUser), payload); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), session)
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("the key name was rendered as live markup")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the key name does not appear in escaped form")
	}
}

func TestRevokeFlow(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	created, err := h.store.CreateKey(t.Context(), testOwner(auth.FakeUser), "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// The confirmation step comes first.
	confirm := h.do(t, httptest.NewRequest(http.MethodGet, "/keys/"+created.ID+"/revoke", nil), session)
	if confirm.Code != http.StatusOK {
		t.Fatalf("GET revoke confirmation = %d, want 200", confirm.Code)
	}
	if !strings.Contains(confirm.Body.String(), "MacBook") {
		t.Error("the confirmation page does not name the key")
	}

	// Then Post/Redirect/Get, so a refresh cannot resubmit.
	rec := h.do(t, postForm("/keys/"+created.ID+"/revoke", nil), session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST revoke = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/account" {
		t.Errorf("Location = %q, want /account", got)
	}

	keys, err := h.store.ListKeys(t.Context(), testOwner(auth.FakeUser).ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("%d keys remain after revoke, want 0", len(keys))
	}
}

// The central authorization rule: a key is reachable only by its owner, and a
// non-owner cannot tell a foreign key from a missing one.
func TestAnotherUserCannotRevoke(t *testing.T) {
	h := newHarness(t)

	victim := keystore.Owner{TenantID: "tenant-a", ObjectID: "oid-victim"}
	created, err := h.store.CreateKey(t.Context(), victim, "Victim key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	attacker := h.signIn(t, auth.SessionUser{TenantID: "tenant-a", ObjectID: "oid-attacker"})

	confirm := h.do(t, httptest.NewRequest(http.MethodGet, "/keys/"+created.ID+"/revoke", nil), attacker)
	if confirm.Code != http.StatusNotFound {
		t.Errorf("GET another user's revoke page = %d, want 404", confirm.Code)
	}
	if strings.Contains(confirm.Body.String(), "Victim key") {
		t.Error("the 404 page leaked another user's key name")
	}

	rec := h.do(t, postForm("/keys/"+created.ID+"/revoke", nil), attacker)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST another user's revoke = %d, want 404", rec.Code)
	}

	if _, err := h.store.GetKey(t.Context(), victim.ID(), created.ID); err != nil {
		t.Errorf("the victim's key was affected: %v", err)
	}
}

// A same-object-ID user in a different tenant is a different person.
func TestCrossTenantIsolation(t *testing.T) {
	h := newHarness(t)

	victim := keystore.Owner{TenantID: "tenant-a", ObjectID: "shared-oid"}
	created, err := h.store.CreateKey(t.Context(), victim, "Tenant A key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	other := h.signIn(t, auth.SessionUser{TenantID: "tenant-b", ObjectID: "shared-oid"})

	rec := h.do(t, postForm("/keys/"+created.ID+"/revoke", nil), other)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant revoke = %d, want 404", rec.Code)
	}
	account := h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), other)
	if strings.Contains(account.Body.String(), "Tenant A key") {
		t.Error("a user in another tenant can see the key")
	}
}

// Cross-origin state changes are rejected by the stdlib protection, which
// replaces per-form synchroniser tokens.
func TestCrossOriginMutationIsBlocked(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	for _, site := range []string{"cross-site", "same-site"} {
		req := postForm("/keys", url.Values{"name": {"Attacker key"}})
		req.Header.Set("Sec-Fetch-Site", site)

		rec := h.do(t, req, session)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST with Sec-Fetch-Site=%s = %d, want 403", site, rec.Code)
		}
	}

	keys, err := h.store.ListKeys(t.Context(), testOwner(auth.FakeUser).ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Error("a cross-origin request created a key")
	}
}

func TestCrossOriginByOriginHeaderIsBlocked(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	// An older browser sends no Sec-Fetch-Site, so Origin is compared to Host.
	req := httptest.NewRequest(http.MethodPost, "/keys",
		strings.NewReader(url.Values{"name": {"Attacker key"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")

	if rec := h.do(t, req, session); rec.Code != http.StatusForbidden {
		t.Errorf("POST from a foreign Origin = %d, want 403", rec.Code)
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	rec := h.do(t, postForm("/logout", nil), session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d, want 303", rec.Code)
	}

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.Name && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the session cookie was not expired")
	}
}

// The ?next= parameter must not become an open redirect.
func TestLoginReturnPathIsSameSiteOnly(t *testing.T) {
	h := newHarness(t)

	for _, next := range []string{
		"https://evil.example/phish",
		"//evil.example/phish",
		"http://evil.example",
	} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, "/login?next="+url.QueryEscape(next), nil), nil)
		if got := rec.Header().Get("Location"); strings.Contains(got, "evil.example") {
			t.Errorf("next=%q produced Location %q", next, got)
		}
	}

	// A same-site path is honoured.
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/login?next=%2Faccount", nil), nil)
	if got := rec.Header().Get("Location"); got != "/account" {
		t.Errorf("Location = %q, want /account", got)
	}
}

func TestAccountPageShowsKeysAndGuides(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	if _, err := h.store.CreateKey(t.Context(), testOwner(auth.FakeUser), "Workstation"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /account = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Workstation") {
		t.Error("the key is not listed")
	}
	for _, guide := range []string{"Claude Code", "Pi", "OpenCode", "Codex", "Crush"} {
		if !strings.Contains(body, guide) {
			t.Errorf("the %s setup guide is missing", guide)
		}
	}
	// The account page reflects the session, so it must not be cached.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// ?key= chooses which key's detail pane is rendered. It is a display
// preference, so an id nobody owns and an id somebody else owns must both fall
// back to the default silently: erroring on one and not the other would report
// whether a key exists.
func TestAccountKeySelectionFallsBackSilently(t *testing.T) {
	h := newHarness(t)
	session := h.signIn(t, auth.FakeUser)

	older, err := h.store.CreateKey(t.Context(), testOwner(auth.FakeUser), "Older key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	newer, err := h.store.CreateKey(t.Context(), testOwner(auth.FakeUser), "Newer key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	foreign, err := h.store.CreateKey(t.Context(),
		keystore.Owner{TenantID: "tenant-a", ObjectID: "oid-other"}, "Someone else's key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "owned", key: newer.ID},
		{name: "unknown", key: "no-such-key"},
		{name: "another user's", key: foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, httptest.NewRequest(http.MethodGet, "/account?key="+url.QueryEscape(tc.key), nil), session)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /account?key=%s = %d, want 200", tc.key, rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, "Someone else's key") {
				t.Error("the account page rendered another user's key")
			}
			// The user's own keys are all listed whichever one is selected.
			for _, name := range []string{older.Name, newer.Name} {
				if !strings.Contains(body, name) {
					t.Errorf("%q is missing from the listing", name)
				}
			}
		})
	}
}

// selectedKey holds the selection rule the handler depends on, independent of
// how the page happens to render it.
func TestSelectedKey(t *testing.T) {
	keys := []KeyView{{ID: "newest"}, {ID: "older"}}

	tests := []struct {
		name string
		keys []KeyView
		want string
		req  string
	}{
		{name: "owned key is honoured", keys: keys, req: "older", want: "older"},
		{name: "unknown id falls back to the first", keys: keys, req: "not-a-key", want: "newest"},
		{name: "no request falls back to the first", keys: keys, req: "", want: "newest"},
		{name: "no keys selects nothing", keys: nil, req: "older", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectedKey(tt.keys, tt.req); got != tt.want {
				t.Errorf("selectedKey(%v, %q) = %q, want %q", tt.keys, tt.req, got, tt.want)
			}
		})
	}
}

func TestEmptyStateInvitesCreation(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/account", nil), h.signIn(t, auth.FakeUser))

	if !strings.Contains(rec.Body.String(), "do not have an API key yet") {
		t.Error("the empty state message is missing")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/no-such-page", nil), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /no-such-page = %d, want 404", rec.Code)
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/static/app.css", "/static/app.js"} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, path, nil), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

// extractSecret pulls the credential out of the one-time page.
func extractSecret(t *testing.T, body string) string {
	t.Helper()
	const marker = "llm_"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no credential found in the one-time page:\n%s", body)
	}
	rest := body[i:]
	end := strings.IndexAny(rest, "<\" \n\t")
	if end < 0 {
		t.Fatal("could not delimit the credential")
	}
	secret := rest[:end]
	if len(secret) < 20 {
		t.Fatalf("extracted credential %q looks wrong", secret)
	}
	return secret
}
