// Command ai-account is the account portal: it signs people in with Microsoft
// Entra ID and lets them create and revoke API keys, which are stored as
// Kubernetes Secrets for the gateway to enforce.
//
// It is not an inference proxy, not an identity database, and not an admin
// plane. See README.md for the boundaries.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/brand"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/config"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/httpapp"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
	k8sstore "github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore/kubernetes"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore/memory"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
	"github.com/dbirks/kubernetes-llm-api-key-portal/web"
)

// Server timeouts. Generous enough for a slow mobile connection, tight enough
// that a stalled peer cannot hold a connection open indefinitely.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownGrace     = 20 * time.Second

	// discoveryTimeout bounds the OIDC discovery call made at startup.
	discoveryTimeout = 20 * time.Second
)

// version is stamped at build time with -ldflags "-X main.version=...".
// It is logged once at startup so a running pod can be tied back to a commit.
var version = "dev"

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so this goes
		// straight to stderr.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}

	log := newLogger(cfg.LogLevel)

	if cfg.DevFakeAuth {
		// Loud, every start. This mode must never be mistaken for production.
		log.Warn("DEVELOPMENT MODE: sign-in is bypassed and every visitor is the same fake user")
	}

	brandData, err := brand.Resolve(cfg.Brand, log)
	if err != nil {
		return fmt.Errorf("branding: %w", err)
	}

	secureCookies := cfg.PublicBaseURL.Scheme == "https"
	sealer, err := auth.NewSealer(cfg.SessionKeys, secureCookies)
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}

	store, err := buildKeyStore(cfg, log)
	if err != nil {
		return err
	}

	authProvider, err := buildAuthProvider(cfg, sealer, log)
	if err != nil {
		return err
	}

	assets, reload, err := buildAssets(log)
	if err != nil {
		return err
	}

	app, err := httpapp.New(httpapp.Options{
		Log:             log,
		Brand:           brandData,
		Store:           store,
		Sealer:          sealer,
		Auth:            authProvider,
		Assets:          assets,
		ReloadTemplates: reload,
		SecureCookies:   secureCookies,
		DevMode:         cfg.DevFakeAuth,
		Onboarding: onboarding.Params{
			BaseURL:   cfg.InferenceBaseURL.String(),
			Model:     cfg.DefaultModel,
			BrandName: brandData.Name,
		},
	})
	if err != nil {
		return fmt.Errorf("build application: %w", err)
	}

	return serve(app, cfg, log)
}

func buildKeyStore(cfg *config.Config, log *slog.Logger) (keystore.KeyStore, error) {
	switch cfg.KeystoreMode {
	case config.KeystoreKubernetes:
		client, err := k8sstore.NewClient(k8sstore.ClientOptions{
			Kubeconfig:      cfg.KubernetesKubeconfig,
			AllowKubeconfig: cfg.KubernetesAllowKubecfg,
		})
		if err != nil {
			return nil, fmt.Errorf("kubernetes keystore: %w", err)
		}
		store, err := k8sstore.New(k8sstore.Options{
			Client:    client,
			Namespace: cfg.KubernetesNamespace,
			KeyPrefix: cfg.APIKeyPrefix,
		})
		if err != nil {
			return nil, fmt.Errorf("kubernetes keystore: %w", err)
		}
		log.Info("using kubernetes keystore",
			"namespace", cfg.KubernetesNamespace,
			"label_domain", k8sstore.LabelDomain)
		return store, nil

	default:
		log.Warn("using in-memory keystore; keys are lost on restart and are not usable for real inference")
		store := memory.New(cfg.APIKeyPrefix)
		if cfg.DevFakeAuth {
			seedDevKeys(store)
		}
		return store, nil
	}
}

// seedDevKeys gives the development account page something to render, so UI
// work does not start from an empty state.
func seedDevKeys(store *memory.Store) {
	owner := keystore.Owner{
		TenantID:    auth.FakeUser.TenantID,
		ObjectID:    auth.FakeUser.ObjectID,
		DisplayName: auth.FakeUser.Name,
		Email:       auth.FakeUser.Email,
	}
	now := time.Now()
	store.Seed(owner, "MacBook / Claude Code", "A1b2C3", now.AddDate(0, 0, -12))
	store.Seed(owner, "Workstation", "D4e5F6", now.AddDate(0, 0, -3))
}

func buildAuthProvider(cfg *config.Config, sealer *auth.Sealer, log *slog.Logger) (auth.Provider, error) {
	if cfg.DevFakeAuth {
		return auth.NewFakeAuthenticator(sealer, auth.FakeUser), nil
	}

	// Discovery is a network call. Doing it at startup means a tenant
	// misconfiguration is a failed rollout rather than a broken login page.
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	authenticator, err := auth.NewAuthenticator(ctx, auth.AuthenticatorConfig{
		IssuerURL:    cfg.EntraIssuerURL(),
		ClientID:     cfg.EntraClientID,
		ClientSecret: cfg.EntraClientSecret,
		RedirectURL:  cfg.RedirectURL(),
		Tenants:      []string{cfg.EntraTenantID},
	}, sealer)
	if err != nil {
		return nil, fmt.Errorf("entra sign-in: %w", err)
	}
	log.Info("entra sign-in configured",
		"tenant_id", cfg.EntraTenantID,
		"redirect_url", cfg.RedirectURL())
	return authenticator, nil
}

// buildAssets chooses between the embedded asset tree and a directory on disk.
//
// The directory mode exists so template and CSS edits appear on refresh. It is
// opt-in via DEV_ASSETS_DIR and never the default, so a production container
// always serves what was compiled into it.
func buildAssets(log *slog.Logger) (fs.FS, bool, error) {
	dir := strings.TrimSpace(os.Getenv("DEV_ASSETS_DIR"))
	if dir == "" {
		return web.Assets(), false, nil
	}
	assets, err := web.DirAssets(dir)
	if err != nil {
		return nil, false, fmt.Errorf("DEV_ASSETS_DIR: %w", err)
	}
	log.Warn("serving assets from disk with per-request reload", "dir", dir)
	return assets, true, nil
}

func serve(handler http.Handler, cfg *config.Config, log *slog.Logger) error {
	server := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// SIGTERM is what Kubernetes sends before it kills the pod; draining first
	// avoids cutting off an in-flight key creation.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening",
			"addr", server.Addr,
			"public_base_url", cfg.PublicBaseURL.String(),
			"version", version)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received; draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
