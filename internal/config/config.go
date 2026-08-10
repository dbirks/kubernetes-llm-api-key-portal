// Package config loads and validates deployment configuration from the environment.
//
// Every value the service needs is resolved once at startup. Invalid or missing
// production configuration is a startup error, never a runtime surprise.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// KeystoreMode selects which KeyStore implementation the service uses.
type KeystoreMode string

const (
	KeystoreMemory     KeystoreMode = "memory"
	KeystoreKubernetes KeystoreMode = "kubernetes"
)

// Config is the fully resolved configuration for one process.
type Config struct {
	Port          int
	PublicBaseURL *url.URL
	LogLevel      string

	EntraTenantID     string
	EntraClientID     string
	EntraClientSecret string

	// SessionKeys holds one or more 32+ byte secrets. The first is used to seal
	// new sessions; the rest are accepted when opening, which makes rotation a
	// matter of prepending a new key and later dropping the old one.
	SessionKeys [][]byte

	KeystoreMode           KeystoreMode
	KubernetesNamespace    string
	KubernetesAllowKubecfg bool
	KubernetesKubeconfig   string

	APIKeyPrefix     string
	DefaultModel     string
	InferenceBaseURL *url.URL

	Brand BrandConfig

	DevFakeAuth bool
}

// BrandConfig is the raw, unvalidated branding input. internal/brand turns this
// into resolved assets and colors.
type BrandConfig struct {
	Name         string
	ShortName    string
	Tagline      string
	LogoFile     string
	LogoAlt      string
	FaviconFile  string
	Accent       string
	AccentDark   string
	SupportEmail string
	SupportURL   string
}

// Defaults applied when a variable is unset.
const (
	defaultPort         = 8080
	defaultLogLevel     = "info"
	defaultAPIKeyPrefix = "llm_"
	defaultBrandName    = "llm.birks.dev"
	defaultBrandTagline = "Private self-hosted AI endpoint"
	defaultBrandAccent  = "#3b6fd6"

	minSessionKeyBytes = 32
)

// Load reads configuration from the process environment.
//
// It collects every problem it finds rather than stopping at the first, so a
// misconfigured deployment reports all of its errors in a single startup log.
func Load() (*Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	cfg := &Config{
		LogLevel:     lookupDefault("LOG_LEVEL", defaultLogLevel),
		APIKeyPrefix: lookupDefault("API_KEY_PREFIX", defaultAPIKeyPrefix),
		DefaultModel: strings.TrimSpace(os.Getenv("DEFAULT_MODEL")),

		EntraTenantID:     strings.TrimSpace(os.Getenv("ENTRA_TENANT_ID")),
		EntraClientID:     strings.TrimSpace(os.Getenv("ENTRA_CLIENT_ID")),
		EntraClientSecret: os.Getenv("ENTRA_CLIENT_SECRET"),

		KubernetesNamespace:    strings.TrimSpace(os.Getenv("KUBERNETES_NAMESPACE")),
		KubernetesKubeconfig:   strings.TrimSpace(os.Getenv("KUBECONFIG")),
		KubernetesAllowKubecfg: boolEnv("KUBERNETES_ALLOW_KUBECONFIG"),

		DevFakeAuth: boolEnv("DEV_FAKE_AUTH"),

		Brand: BrandConfig{
			Name:         lookupDefault("BRAND_NAME", defaultBrandName),
			ShortName:    strings.TrimSpace(os.Getenv("BRAND_SHORT_NAME")),
			Tagline:      lookupDefault("BRAND_TAGLINE", defaultBrandTagline),
			LogoFile:     strings.TrimSpace(os.Getenv("BRAND_LOGO_FILE")),
			LogoAlt:      strings.TrimSpace(os.Getenv("BRAND_LOGO_ALT")),
			FaviconFile:  strings.TrimSpace(os.Getenv("BRAND_FAVICON_FILE")),
			Accent:       lookupDefault("BRAND_ACCENT", defaultBrandAccent),
			AccentDark:   strings.TrimSpace(os.Getenv("BRAND_ACCENT_DARK")),
			SupportEmail: strings.TrimSpace(os.Getenv("BRAND_SUPPORT_EMAIL")),
			SupportURL:   strings.TrimSpace(os.Getenv("BRAND_SUPPORT_URL")),
		},
	}
	// The dark derivation in internal/brand lightens an accent until it clears
	// AA against the page surface, which is the right guard for an accent used
	// as text. Ours is only ever a button fill (4.74:1 with white), a 2px nav
	// underline, and a focus ring — non-text, where the floor is 3:1. So for
	// our own default, skip the derivation and ship the designed value. An
	// operator who sets BRAND_ACCENT still gets the protection.
	if os.Getenv("BRAND_ACCENT") == "" && cfg.Brand.AccentDark == "" {
		cfg.Brand.AccentDark = defaultBrandAccent
	}

	if cfg.Brand.ShortName == "" {
		cfg.Brand.ShortName = cfg.Brand.Name
	}
	if cfg.Brand.LogoAlt == "" {
		cfg.Brand.LogoAlt = cfg.Brand.Name
	}

	// Port.
	cfg.Port = defaultPort
	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			fail("PORT must be a number between 1 and 65535, got %q", raw)
		} else {
			cfg.Port = port
		}
	}

	// Public base URL. This is the canonical external origin; we never derive it
	// from forwarded headers, which are attacker-controlled behind a tunnel.
	rawBase := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	if rawBase == "" {
		fail("PUBLIC_BASE_URL is required (e.g. https://ai.example.com)")
	} else if base, err := parseBaseURL(rawBase); err != nil {
		fail("PUBLIC_BASE_URL: %w", err)
	} else {
		cfg.PublicBaseURL = base
	}

	// Inference base URL defaults to the portal origin, but the two can diverge.
	if raw := strings.TrimSpace(os.Getenv("INFERENCE_BASE_URL")); raw != "" {
		if inference, err := parseBaseURL(raw); err != nil {
			fail("INFERENCE_BASE_URL: %w", err)
		} else {
			cfg.InferenceBaseURL = inference
		}
	} else {
		cfg.InferenceBaseURL = cfg.PublicBaseURL
	}

	// Keystore mode.
	switch mode := KeystoreMode(lookupDefault("KEYSTORE_MODE", string(KeystoreMemory))); mode {
	case KeystoreMemory, KeystoreKubernetes:
		cfg.KeystoreMode = mode
	default:
		fail("KEYSTORE_MODE must be %q or %q, got %q", KeystoreMemory, KeystoreKubernetes, mode)
	}
	if cfg.KeystoreMode == KeystoreKubernetes && cfg.KubernetesNamespace == "" {
		fail("KUBERNETES_NAMESPACE is required when KEYSTORE_MODE=kubernetes")
	}

	if cfg.APIKeyPrefix == "" {
		fail("API_KEY_PREFIX must not be empty")
	}

	// Authentication. The dev bypass replaces Entra entirely, so its
	// requirements only apply to real deployments.
	if !cfg.DevFakeAuth {
		if cfg.EntraTenantID == "" {
			fail("ENTRA_TENANT_ID is required")
		}
		if cfg.EntraClientID == "" {
			fail("ENTRA_CLIENT_ID is required")
		}
		if cfg.EntraClientSecret == "" {
			fail("ENTRA_CLIENT_SECRET is required")
		}
	}

	keys, err := parseSessionKeys(os.Getenv("SESSION_KEY"))
	if err != nil {
		fail("SESSION_KEY: %w", err)
	} else {
		cfg.SessionKeys = keys
	}

	errs = append(errs, cfg.checkDevGuards()...)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// checkDevGuards enforces that the development login bypass cannot be switched
// on in anything resembling a production deployment. The brief requires it to be
// impossible to enable accidentally, so these are hard errors, not warnings.
func (c *Config) checkDevGuards() []error {
	if !c.DevFakeAuth {
		return nil
	}
	var errs []error
	if c.KeystoreMode == KeystoreKubernetes {
		errs = append(errs, errors.New("DEV_FAKE_AUTH must not be combined with KEYSTORE_MODE=kubernetes"))
	}
	if c.PublicBaseURL != nil && !isLoopback(c.PublicBaseURL.Hostname()) {
		errs = append(errs, fmt.Errorf("DEV_FAKE_AUTH requires a loopback PUBLIC_BASE_URL, got host %q", c.PublicBaseURL.Hostname()))
	}
	return errs
}

// EntraIssuerURL is the OIDC discovery issuer for the configured tenant.
func (c *Config) EntraIssuerURL() string {
	return "https://login.microsoftonline.com/" + c.EntraTenantID + "/v2.0"
}

// RedirectURL is the absolute OAuth callback derived from the public origin.
func (c *Config) RedirectURL() string {
	return strings.TrimSuffix(c.PublicBaseURL.String(), "/") + "/auth/callback"
}

// Addr is the listen address for the HTTP server.
func (c *Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("missing host")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, errors.New("http is only allowed for loopback hosts; use https")
	}
	// Normalise: we only ever want scheme://host[:port] with no trailing slash.
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimSuffix(u.Path, "/")}, nil
}

// parseSessionKeys accepts a comma-separated list of base64 (standard or raw
// URL) encoded secrets. The first entry seals new sessions; later entries are
// only used to open existing ones.
func parseSessionKeys(raw string) ([][]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("required; generate one with: openssl rand -base64 32")
	}
	var keys [][]byte
	for i, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, err := decodeBase64(part)
		if err != nil {
			return nil, fmt.Errorf("key %d is not valid base64", i+1)
		}
		if len(key) < minSessionKeyBytes {
			return nil, fmt.Errorf("key %d decodes to %d bytes, need at least %d", i+1, len(key), minSessionKeyBytes)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("required; generate one with: openssl rand -base64 32")
	}
	return keys, nil
}

func decodeBase64(s string) ([]byte, error) {
	if key, err := base64.StdEncoding.DecodeString(s); err == nil {
		return key, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func lookupDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func boolEnv(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && v
}
