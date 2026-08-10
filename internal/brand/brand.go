// Package brand turns operator-supplied branding configuration into resolved
// assets and a generated stylesheet.
//
// The design constraint that shapes this package: the portal serves a strict
// Content-Security-Policy with style-src 'self', and we do not want a nonce.
// That rules out injecting the accent color as an inline style attribute or a
// <style> block. Instead the accent is compiled once at startup into a tiny
// content-addressed stylesheet that defines CSS custom properties, which the
// hand-written stylesheet consumes with fallbacks.
package brand

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/config"
)

// Prefixes for the asset routes this package owns.
const (
	logoURLPrefix    = "/assets/brand/logo-"
	faviconURLPrefix = "/assets/brand/icon-"
	cssURLPrefix     = "/assets/brand-"
)

// maxNameLen bounds the operator-supplied display strings. Templates escape
// these, so this is about layout sanity rather than injection.
const maxNameLen = 64

// Brand is the resolved, template-facing branding for this deployment.
// It is immutable after startup and safe to share across requests.
type Brand struct {
	Name      string
	ShortName string
	Tagline   string

	// OrgName is the organisation whose work accounts sign in, used in the
	// sign-in heading. It is separate from Name because the two answer
	// different questions: Name is what this service is called, OrgName is
	// whose account you are being asked for. May be empty.
	OrgName string

	HasLogo bool
	LogoURL string
	LogoAlt string

	FaviconURL string

	SupportEmail string
	SupportURL   string

	// StylesheetURL is the generated custom-property sheet. Templates must
	// include it before the main stylesheet.
	StylesheetURL string

	logo       *Asset
	favicon    *Asset
	stylesheet []byte
}

// Resolve validates branding configuration and compiles the generated assets.
//
// Invalid input is a startup error rather than a silent fallback: an operator
// who mounted a broken logo should hear about it immediately, not discover a
// missing image in production. The one exception is a low-contrast accent
// color, which is a taste judgement we warn about but do not override.
func Resolve(cfg config.BrandConfig, log *slog.Logger) (*Brand, error) {
	name, err := cleanText(cfg.Name, "BRAND_NAME")
	if err != nil {
		return nil, err
	}
	shortName, err := cleanText(orDefault(cfg.ShortName, name), "BRAND_SHORT_NAME")
	if err != nil {
		return nil, err
	}
	tagline, err := cleanText(cfg.Tagline, "BRAND_TAGLINE")
	if err != nil {
		return nil, err
	}
	// Optional: an empty OrgName means the sign-in page drops the "your X
	// account" framing rather than failing to start. Anyone constructing a
	// BrandConfig directly, tests included, gets that for free.
	orgName, err := cleanOptionalText(cfg.OrgName, "BRAND_ORG_NAME")
	if err != nil {
		return nil, err
	}
	logoAlt, err := cleanText(orDefault(cfg.LogoAlt, name), "BRAND_LOGO_ALT")
	if err != nil {
		return nil, err
	}

	b := &Brand{
		Name:         name,
		ShortName:    shortName,
		Tagline:      tagline,
		OrgName:      orgName,
		LogoAlt:      logoAlt,
		SupportEmail: cfg.SupportEmail,
		SupportURL:   cfg.SupportURL,
	}

	if cfg.LogoFile != "" {
		if b.logo, err = loadAsset(cfg.LogoFile, logoURLPrefix); err != nil {
			return nil, fmt.Errorf("BRAND_LOGO_FILE: %w", err)
		}
		b.HasLogo = true
		b.LogoURL = b.logo.URLPath
	}
	if cfg.FaviconFile != "" {
		if b.favicon, err = loadAsset(cfg.FaviconFile, faviconURLPrefix); err != nil {
			return nil, fmt.Errorf("BRAND_FAVICON_FILE: %w", err)
		}
		b.FaviconURL = b.favicon.URLPath
	}

	accent, err := ParseHex(cfg.Accent)
	if err != nil {
		return nil, fmt.Errorf("BRAND_ACCENT: %w", err)
	}
	darkAccent, explicitDark := accent, false
	if cfg.AccentDark != "" {
		if darkAccent, err = ParseHex(cfg.AccentDark); err != nil {
			return nil, fmt.Errorf("BRAND_ACCENT_DARK: %w", err)
		}
		explicitDark = true
	}

	palette := darkPalette(darkAccent, explicitDark)
	if palette.Contrast < minContrast {
		log.Warn("brand accent has low text contrast; buttons using it may be hard to read",
			"accent", palette.Accent.Hex(),
			"contrast_ratio", fmt.Sprintf("%.2f", palette.Contrast),
			"recommended_minimum", minContrast)
	}

	b.stylesheet = []byte(renderCSS(palette))
	sum := sha256.Sum256(b.stylesheet)
	b.StylesheetURL = cssURLPrefix + hex.EncodeToString(sum[:])[:12] + ".css"
	return b, nil
}

// renderCSS emits only custom properties. All values are derived from validated
// hex colors, so nothing operator-supplied reaches the stylesheet as free text.
//
// One palette, at bare :root, with no prefers-color-scheme query. The portal is
// dark-only and declares `color-scheme: dark`, so publishing a light palette as
// the default would hand light-scheme accents to every visitor whose operating
// system is set to light — on a page that is dark regardless.
func renderCSS(p Palette) string {
	var sb strings.Builder
	sb.WriteString("/* generated at startup from BRAND_ACCENT; do not edit */\n")
	sb.WriteString(":root {\n")
	fmt.Fprintf(&sb, "  --brand-accent: %s;\n", p.Accent.Hex())
	fmt.Fprintf(&sb, "  --brand-accent-hover: %s;\n", p.AccentHover.Hex())
	fmt.Fprintf(&sb, "  --brand-accent-fg: %s;\n", p.AccentFg.Hex())
	fmt.Fprintf(&sb, "  --brand-accent-subtle: %s;\n", p.Subtle.Hex())
	sb.WriteString("}\n")
	return sb.String()
}

// Handler serves the generated stylesheet and any operator-supplied images.
// Paths are content-addressed, so an unknown hash is simply a 404.
func (b *Brand) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+b.StylesheetURL, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(b.stylesheet)
	})
	for _, a := range []*Asset{b.logo, b.favicon} {
		if a == nil {
			continue
		}
		asset := a
		mux.HandleFunc("GET "+asset.URLPath, func(w http.ResponseWriter, r *http.Request) {
			ServeAsset(w, asset)
		})
	}
	return mux
}

// Stylesheet exposes the generated CSS for tests.
func (b *Brand) Stylesheet() []byte { return b.stylesheet }

// cleanText validates an operator-supplied display string. Control characters
// are rejected outright: they cannot render usefully and they make log output
// and terminal debugging confusing.
// cleanOptionalText is cleanText for strings that may legitimately be absent.
func cleanOptionalText(s, field string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	return cleanText(s, field)
}

func cleanText(s, field string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	if len([]rune(s)) > maxNameLen {
		return "", fmt.Errorf("%s must be at most %d characters", field, maxNameLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s must not contain control characters", field)
		}
	}
	// Guard against an operator pasting pre-escaped HTML and getting double
	// escaping in the rendered page.
	if unescaped := html.UnescapeString(s); unescaped != s {
		return "", fmt.Errorf("%s must be plain text, not HTML-escaped", field)
	}
	return s, nil
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
