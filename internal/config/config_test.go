package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

// setEnv applies a configuration to the environment for one test, clearing
// every variable the loader reads so tests cannot leak into each other.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	known := []string{
		"PORT", "PUBLIC_BASE_URL", "LOG_LEVEL",
		"ENTRA_TENANT_ID", "ENTRA_CLIENT_ID", "ENTRA_CLIENT_SECRET", "ENTRA_AVATARS",
		"SESSION_KEY", "KEYSTORE_MODE", "KUBERNETES_NAMESPACE",
		"KUBERNETES_ALLOW_KUBECONFIG", "KUBECONFIG",
		"API_KEY_PREFIX", "DEFAULT_MODEL", "INFERENCE_BASE_URL", "DEV_FAKE_AUTH",
		"BRAND_NAME", "BRAND_SHORT_NAME", "BRAND_TAGLINE", "BRAND_LOGO_FILE",
		"BRAND_LOGO_ALT", "BRAND_FAVICON_FILE", "BRAND_ACCENT", "BRAND_ACCENT_DARK",
		"BRAND_SUPPORT_EMAIL", "BRAND_SUPPORT_URL",
	}
	for _, k := range known {
		t.Setenv(k, "")
		_ = k
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func validKey() string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
}

func productionEnv() map[string]string {
	return map[string]string{
		"PUBLIC_BASE_URL":     "https://llm.birks.dev",
		"ENTRA_TENANT_ID":     "11111111-1111-1111-1111-111111111111",
		"ENTRA_CLIENT_ID":     "22222222-2222-2222-2222-222222222222",
		"ENTRA_CLIENT_SECRET": "shhh",
		"SESSION_KEY":         validKey(),
	}
}

func TestLoadProductionDefaults(t *testing.T) {
	setEnv(t, productionEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Port != defaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, defaultPort)
	}
	// Memory is the default so that a mistake never writes Secrets to a cluster.
	if cfg.KeystoreMode != KeystoreMemory {
		t.Errorf("KeystoreMode = %q, want %q", cfg.KeystoreMode, KeystoreMemory)
	}
	if cfg.APIKeyPrefix != defaultAPIKeyPrefix {
		t.Errorf("APIKeyPrefix = %q, want %q", cfg.APIKeyPrefix, defaultAPIKeyPrefix)
	}
	if got, want := cfg.RedirectURL(), "https://llm.birks.dev/auth/callback"; got != want {
		t.Errorf("RedirectURL() = %q, want %q", got, want)
	}
	if got, want := cfg.EntraIssuerURL(), "https://login.microsoftonline.com/"+cfg.EntraTenantID+"/v2.0"; got != want {
		t.Errorf("EntraIssuerURL() = %q, want %q", got, want)
	}
	// Inference defaults to the portal origin when not split out.
	if cfg.InferenceBaseURL.String() != cfg.PublicBaseURL.String() {
		t.Errorf("InferenceBaseURL = %q, want it to default to the public origin", cfg.InferenceBaseURL)
	}
}

// A misconfigured deployment should report every problem at once rather than
// one per restart.
func TestLoadReportsAllProblems(t *testing.T) {
	setEnv(t, map[string]string{})

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded with no configuration at all")
	}
	msg := err.Error()
	for _, want := range []string{"PUBLIC_BASE_URL", "ENTRA_TENANT_ID", "ENTRA_CLIENT_ID", "ENTRA_CLIENT_SECRET", "SESSION_KEY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %s:\n%s", want, msg)
		}
	}
}

func TestSecretsHaveNoProductionDefault(t *testing.T) {
	for _, missing := range []string{"ENTRA_CLIENT_SECRET", "SESSION_KEY"} {
		t.Run(missing, func(t *testing.T) {
			env := productionEnv()
			delete(env, missing)
			setEnv(t, env)

			if _, err := Load(); err == nil {
				t.Errorf("Load succeeded without %s", missing)
			}
		})
	}
}

func TestSessionKeyValidation(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		wantN   int
	}{
		{name: "standard base64", value: validKey(), wantN: 1},
		{name: "raw url base64", value: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), wantN: 1},
		{name: "rotation list", value: validKey() + "," + validKey(), wantN: 2},
		{name: "too short", value: base64.StdEncoding.EncodeToString([]byte("short")), wantErr: true},
		{name: "not base64", value: "!!!not base64!!!", wantErr: true},
		{name: "empty", value: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := productionEnv()
			env["SESSION_KEY"] = tt.value
			setEnv(t, env)

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Error("Load accepted an invalid SESSION_KEY")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.SessionKeys) != tt.wantN {
				t.Errorf("got %d session keys, want %d", len(cfg.SessionKeys), tt.wantN)
			}
		})
	}
}

func TestPublicBaseURLValidation(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{value: "https://llm.birks.dev"},
		{value: "https://llm.birks.dev/"},
		{value: "http://localhost:8080"},
		{value: "http://127.0.0.1:8080"},
		// Plain http off-loopback would mean cookies without Secure in production.
		{value: "http://llm.birks.dev", wantErr: true},
		{value: "ftp://llm.birks.dev", wantErr: true},
		{value: "llm.birks.dev", wantErr: true},
		{value: "https://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			env := productionEnv()
			env["PUBLIC_BASE_URL"] = tt.value
			setEnv(t, env)

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Load accepted PUBLIC_BASE_URL=%q", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			// A trailing slash must be normalised away so the redirect URL is
			// not built with a double slash.
			if strings.HasSuffix(cfg.PublicBaseURL.String(), "/") {
				t.Errorf("PublicBaseURL = %q, want no trailing slash", cfg.PublicBaseURL)
			}
		})
	}
}

func TestKubernetesModeRequiresANamespace(t *testing.T) {
	env := productionEnv()
	env["KEYSTORE_MODE"] = "kubernetes"
	setEnv(t, env)

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KUBERNETES_NAMESPACE") {
		t.Errorf("Load = %v, want a complaint about KUBERNETES_NAMESPACE", err)
	}

	env["KUBERNETES_NAMESPACE"] = "llm-access"
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeystoreMode != KeystoreKubernetes {
		t.Errorf("KeystoreMode = %q, want kubernetes", cfg.KeystoreMode)
	}
}

// The development bypass must be impossible to enable by accident anywhere that
// looks like a real deployment.
func TestDevFakeAuthGuardRails(t *testing.T) {
	t.Run("rejected on a public origin", func(t *testing.T) {
		env := productionEnv()
		env["DEV_FAKE_AUTH"] = "true"
		setEnv(t, env)

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("Load = %v, want a refusal because the origin is public", err)
		}
	})

	t.Run("rejected with the kubernetes keystore", func(t *testing.T) {
		setEnv(t, map[string]string{
			"PUBLIC_BASE_URL":      "http://localhost:8080",
			"SESSION_KEY":          validKey(),
			"DEV_FAKE_AUTH":        "true",
			"KEYSTORE_MODE":        "kubernetes",
			"KUBERNETES_NAMESPACE": "llm-access",
		})
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KEYSTORE_MODE=kubernetes") {
			t.Errorf("Load = %v, want a refusal because the keystore is real", err)
		}
	})

	t.Run("allowed on loopback with the memory store", func(t *testing.T) {
		setEnv(t, map[string]string{
			"PUBLIC_BASE_URL": "http://localhost:8080",
			"SESSION_KEY":     validKey(),
			"DEV_FAKE_AUTH":   "true",
		})
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.DevFakeAuth {
			t.Error("DevFakeAuth was not enabled")
		}
		// Entra configuration is not required, since it is bypassed entirely.
		if cfg.EntraClientSecret != "" {
			t.Error("an Entra secret was picked up in dev mode")
		}
	})
}

func TestPortValidation(t *testing.T) {
	for _, value := range []string{"0", "-1", "70000", "eight thousand"} {
		env := productionEnv()
		env["PORT"] = value
		setEnv(t, env)

		if _, err := Load(); err == nil {
			t.Errorf("Load accepted PORT=%q", value)
		}
	}
}

func TestInferenceBaseURLCanDiffer(t *testing.T) {
	env := productionEnv()
	env["INFERENCE_BASE_URL"] = "https://llm.birks.dev"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InferenceBaseURL.String() != "https://llm.birks.dev" {
		t.Errorf("InferenceBaseURL = %q", cfg.InferenceBaseURL)
	}
	if cfg.PublicBaseURL.String() != "https://llm.birks.dev" {
		t.Errorf("PublicBaseURL was overwritten: %q", cfg.PublicBaseURL)
	}
}

func TestBrandDefaults(t *testing.T) {
	setEnv(t, productionEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The default is this deployment's own identity, not a generic placeholder.
	if cfg.Brand.Name != "llm.birks.dev" {
		t.Errorf("Brand.Name = %q, want the built-in default", cfg.Brand.Name)
	}
	// ShortName and LogoAlt fall back to the name so templates never render
	// an empty label.
	if cfg.Brand.ShortName != cfg.Brand.Name || cfg.Brand.LogoAlt != cfg.Brand.Name {
		t.Errorf("brand fallbacks not applied: %+v", cfg.Brand)
	}
	if cfg.Brand.Accent != defaultBrandAccent {
		t.Errorf("Brand.Accent = %q, want %q", cfg.Brand.Accent, defaultBrandAccent)
	}
}

func TestBrandOverrides(t *testing.T) {
	env := productionEnv()
	env["BRAND_NAME"] = "Acme AI"
	env["BRAND_SHORT_NAME"] = "Acme"
	env["BRAND_ACCENT"] = "#0f766e"
	env["BRAND_SUPPORT_EMAIL"] = "help@birks.dev"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Brand.Name != "Acme AI" || cfg.Brand.ShortName != "Acme" {
		t.Errorf("brand names not applied: %+v", cfg.Brand)
	}
	// LogoAlt still falls back to the full name, not the short one.
	if cfg.Brand.LogoAlt != "Acme AI" {
		t.Errorf("Brand.LogoAlt = %q, want the full brand name", cfg.Brand.LogoAlt)
	}
	if cfg.Brand.Accent != "#0f766e" || cfg.Brand.SupportEmail != "help@birks.dev" {
		t.Errorf("brand overrides not applied: %+v", cfg.Brand)
	}
}

// Avatars are on unless switched off, so an existing deployment picks them up
// without a config change.
func TestAvatarsDefaultOn(t *testing.T) {
	setEnv(t, productionEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EntraAvatars {
		t.Error("EntraAvatars is false by default, want true")
	}
}

func TestAvatarsCanBeDisabled(t *testing.T) {
	env := productionEnv()
	env["ENTRA_AVATARS"] = "false"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EntraAvatars {
		t.Error("EntraAvatars is true, want false when ENTRA_AVATARS=false")
	}
}

// A typo must not silently switch the feature off, which is what a bare
// ParseBool would do.
func TestUnparseableAvatarsSettingKeepsTheDefault(t *testing.T) {
	for _, raw := range []string{"yes please", "off!", "  "} {
		env := productionEnv()
		env["ENTRA_AVATARS"] = raw
		setEnv(t, env)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load with ENTRA_AVATARS=%q: %v", raw, err)
		}
		if !cfg.EntraAvatars {
			t.Errorf("ENTRA_AVATARS=%q disabled avatars, want the default to hold", raw)
		}
	}
}
