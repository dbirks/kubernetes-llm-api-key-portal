// Package httpapp is the web layer: routing, handlers, middleware, and the
// view models the templates render.
//
// It depends on the auth and keystore interfaces rather than their
// implementations, which is what allows the whole surface to be tested with an
// in-memory store and a fake authenticator.
package httpapp

import (
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/avatar"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/brand"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
)

// PhotoStore reads cached profile photos.
//
// Declared here rather than imported as a concrete type so that the web layer
// depends on the capability and not on the Graph client behind it, matching how
// it already treats the keystore and the auth provider.
type PhotoStore interface {
	Get(key string) (avatar.Photo, bool)
	Forget(key string)
}

// Options configures the application.
type Options struct {
	Log    *slog.Logger
	Brand  *brand.Brand
	Store  keystore.KeyStore
	Sealer *auth.Sealer
	Auth   auth.Provider

	// Assets holds templates/ and static/ subtrees.
	Assets fs.FS

	// ReloadTemplates re-parses templates on every request, for development.
	ReloadTemplates bool

	// Onboarding parameterises the generated client setup guides.
	Onboarding onboarding.Params

	// Photos serves cached profile photos. Nil disables avatars, and every
	// user falls back to an initials badge.
	Photos PhotoStore

	// SecureCookies mirrors whether the public origin is https.
	SecureCookies bool

	// DevMode indicates the development login bypass is active, which every
	// page must advertise.
	DevMode bool
}

// App is the assembled web application.
type App struct {
	log      *slog.Logger
	brand    *brand.Brand
	store    keystore.KeyStore
	sealer   *auth.Sealer
	auth     auth.Provider
	renderer *renderer

	assets     fs.FS
	onboarding onboarding.Params
	photos     PhotoStore

	secureCookies bool
	devMode       bool

	handler http.Handler
}

// New assembles the application and its routing table.
func New(opts Options) (*App, error) {
	if opts.Log == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if opts.Store == nil || opts.Sealer == nil || opts.Auth == nil || opts.Brand == nil {
		return nil, fmt.Errorf("store, sealer, auth provider and brand are required")
	}

	r, err := newRenderer(opts.Assets, opts.ReloadTemplates)
	if err != nil {
		return nil, err
	}

	a := &App{
		log:           opts.Log,
		brand:         opts.Brand,
		store:         opts.Store,
		sealer:        opts.Sealer,
		auth:          opts.Auth,
		renderer:      r,
		assets:        opts.Assets,
		onboarding:    opts.Onboarding,
		photos:        opts.Photos,
		secureCookies: opts.SecureCookies,
		devMode:       opts.DevMode,
	}
	a.handler = a.routes()
	return a, nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }

// routes builds the routing table.
//
// Health endpoints sit outside the security and session middleware: a kubelet
// probe has no session, and a probe failing because of a header bug would be
// its own outage.
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)

	mux.Handle("GET /assets/", a.brand.Handler())
	mux.Handle("GET /static/", a.staticHandler())

	mux.HandleFunc("GET /{$}", a.handleLanding)
	mux.HandleFunc("GET /how-it-works", a.handleHowItWorks)
	mux.HandleFunc("GET /login", a.handleLogin)
	mux.HandleFunc("GET /auth/callback", a.handleCallback)

	protected := func(h http.HandlerFunc) http.Handler {
		return auth.RequireUser(h)
	}
	mux.Handle("GET /account", protected(a.handleAccount))
	mux.Handle("GET /keys/new", protected(a.handleNewKey))
	mux.Handle("POST /keys", protected(a.handleCreateKey))
	mux.Handle("GET /keys/{id}/revoke", protected(a.handleRevokeConfirm))
	mux.Handle("POST /keys/{id}/revoke", protected(a.handleRevokeKey))
	mux.Handle("GET /me/avatar", protected(a.handleAvatar))
	mux.Handle("POST /logout", protected(a.handleLogout))

	// Cross-origin protection covers every state-changing request. Go's
	// implementation checks Sec-Fetch-Site, falling back to comparing Origin
	// against Host, which removes the need for per-form synchroniser tokens.
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.log.Warn("cross-origin request rejected",
			"request_id", RequestIDFrom(r.Context()),
			"path", r.URL.Path,
			"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
		a.renderError(w, r, http.StatusForbidden,
			"That request was blocked",
			"This action has to start from this site. Go back and try again.")
	}))

	return chain(mux,
		withRequestID,
		logRequests(a.log, mux),
		a.recoverPanics,
		a.securityHeaders,
		csrf.Handler,
		auth.LoadSession(a.sealer),
	)
}

// staticHandler serves the first-party stylesheet and script.
func (a *App) staticHandler() http.Handler {
	sub, err := fs.Sub(a.assets, "static")
	if err != nil {
		// The embedded tree is compiled in, so this can only fail if the
		// directory is missing entirely, which is a build-time mistake.
		panic("httpapp: static assets unavailable: " + err.Error())
	}
	// Go's built-in extension table has no .woff2 entry, and the distroless
	// runtime image has no /etc/mime.types for it to fall back to, so the
	// vendored fonts would be served as sniffed application/octet-stream in
	// the container while looking correct on any developer machine.
	// Registering it here makes the two environments agree.
	if err := mime.AddExtensionType(".woff2", "font/woff2"); err != nil {
		panic("httpapp: registering the woff2 media type: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These are versioned by deployment rather than by content hash, so
		// cache them briefly instead of forever.
		w.Header().Set("Cache-Control", "public, max-age=300")
		fileServer.ServeHTTP(w, r)
	}))
}
