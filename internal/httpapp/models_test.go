package httpapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/brand"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/config"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore/memory"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/models"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
	"github.com/dbirks/kubernetes-llm-api-key-portal/web"
)

// fakeCatalog is a models.Catalog backed by a fixed slice or a canned error, so
// a handler test needs no Kubernetes client. It mirrors the fake-store style
// the rest of the suite uses.
type fakeCatalog struct {
	models []models.Model
	err    error
}

func (f fakeCatalog) List(context.Context) ([]models.Model, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

// newModelsHarness assembles an app with the given catalog. A nil catalog is
// the feature-off configuration.
func newModelsHarness(t *testing.T, catalog models.Catalog) *App {
	t.Helper()

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	brandData, err := brand.Resolve(config.BrandConfig{
		Name: "llm.birks.dev", ShortName: "llm.birks.dev", OrgName: "E-gineering",
		Tagline: "Private self-hosted AI endpoint", LogoAlt: "llm.birks.dev", Accent: "#3b6fd6",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("brand.Resolve: %v", err)
	}
	sealer, err := auth.NewSealer([][]byte{bytes.Repeat([]byte{7}, 32)}, false)
	if err != nil {
		t.Fatalf("auth.NewSealer: %v", err)
	}
	app, err := New(Options{
		Log:    log,
		Brand:  brandData,
		Store:  memory.New("llm_"),
		Sealer: sealer,
		Auth:   auth.NewFakeAuthenticator(sealer, auth.FakeUser),
		Models: catalog,
		Assets: web.Assets(),
		Onboarding: onboarding.Params{
			BaseURL: "https://llm.birks.dev", Model: "qwen3.8-nvfp4", BrandName: "llm.birks.dev",
		},
	})
	if err != nil {
		t.Fatalf("httpapp.New: %v", err)
	}
	return app
}

func getPath(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func getModels(t *testing.T, app *App) *httptest.ResponseRecorder {
	t.Helper()
	return getPath(t, app, "/models")
}

// The catalog renders each model's routing name, human status, and the
// cold-start note. It is public, so no session is supplied.
func TestModelsPageRendersCatalog(t *testing.T) {
	app := newModelsHarness(t, fakeCatalog{models: []models.Model{
		{Name: "qwen3.8-nvfp4", DisplayName: "Qwen 3.8", Status: models.StatusReady},
		{Name: "muse-glimmer-30b", DisplayName: "Muse Glimmer", Status: models.StatusScaledToZero},
		{Name: "cold-model", Status: models.StatusLoading},
	}})

	rec := getModels(t, app)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /models = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"qwen3.8-nvfp4", "muse-glimmer-30b", "cold-model",
		"Qwen 3.8", "Muse Glimmer",
		"Ready", "Idle · scaled to zero", "Loading",
		"cold-start",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the models page is missing %q", want)
		}
	}
}

// The Models nav link appears only when a catalog is configured.
func TestModelsNavLinkIsGatedOnTheCatalog(t *testing.T) {
	// The nav link is the one that reads ">Models</a>"; other pages may link to
	// /models in their body copy, so the assertion targets the nav item text.
	const navLink = `>Models</a>`

	withCatalog := newModelsHarness(t, fakeCatalog{})
	rec := getPath(t, withCatalog, "/how-it-works")
	if !strings.Contains(rec.Body.String(), navLink) {
		t.Error("the Models nav link is missing when a catalog is configured")
	}

	withoutCatalog := newModelsHarness(t, nil)
	rec = getPath(t, withoutCatalog, "/how-it-works")
	if strings.Contains(rec.Body.String(), navLink) {
		t.Error("the Models nav link appears with no catalog configured")
	}
}

// With no catalog configured the page renders its disabled state rather than a
// 404, so a shared link still explains itself.
func TestModelsPageDisabledState(t *testing.T) {
	app := newModelsHarness(t, nil)
	rec := getModels(t, app)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /models with no catalog = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "isn't available") {
		t.Error("the disabled state message is missing")
	}
}

// A catalog read failure renders a friendly 503, not a 200 with a broken list.
func TestModelsPageServiceUnavailableOnError(t *testing.T) {
	app := newModelsHarness(t, fakeCatalog{err: errors.New("api server unreachable")})
	rec := getModels(t, app)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /models on error = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "api server unreachable") {
		t.Error("the internal error detail leaked to the page")
	}
	if !strings.Contains(body, "Nothing changed") {
		t.Error("the friendly reassurance is missing")
	}
}

// An empty catalog renders the empty state, distinct from the disabled state.
func TestModelsPageEmptyState(t *testing.T) {
	app := newModelsHarness(t, fakeCatalog{models: nil})
	rec := getModels(t, app)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /models empty = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No models are published yet") {
		t.Error("the empty-catalog message is missing")
	}
}
