package brand

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/config"
)

// A 1x1 PNG, the smallest valid image to exercise the asset path.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func baseConfig() config.BrandConfig {
	return config.BrandConfig{
		Name:      "Birks AI",
		ShortName: "Birks AI",
		Tagline:   "Private self-hosted AI endpoint",
		LogoAlt:   "Birks AI",
		Accent:    "#4f46e5",
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestResolveDefaults(t *testing.T) {
	b, err := Resolve(baseConfig(), quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.HasLogo {
		t.Error("HasLogo is true with no logo configured")
	}
	if !strings.HasPrefix(b.StylesheetURL, "/assets/brand-") || !strings.HasSuffix(b.StylesheetURL, ".css") {
		t.Errorf("StylesheetURL = %q, want a content-addressed css path", b.StylesheetURL)
	}

	css := string(b.Stylesheet())
	for _, want := range []string{
		"--brand-accent:", "--brand-accent-hover:", "--brand-accent-fg:", "--brand-accent-subtle:",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("generated stylesheet is missing %q", want)
		}
	}

	// The portal is dark-only. A prefers-color-scheme query here would mean the
	// properties at bare :root are the light palette, which is what every
	// light-mode visitor would then get on a permanently dark page.
	if strings.Contains(css, "prefers-color-scheme") {
		t.Errorf("generated stylesheet still branches on color scheme:\n%s", css)
	}
}

// The stylesheet must be pure custom properties. Anything else would mean
// operator input is reaching CSS as free text.
func TestStylesheetContainsOnlyColorTokens(t *testing.T) {
	cfg := baseConfig()
	cfg.Name = "Company; } body { display: none } /*"
	b, err := Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	css := string(b.Stylesheet())
	if strings.Contains(css, "display: none") || strings.Contains(css, "Company") {
		t.Errorf("brand name reached the stylesheet:\n%s", css)
	}
}

// The URL is a content hash, so changing the accent must change the URL and
// therefore bust any cached copy.
func TestStylesheetURLTracksContent(t *testing.T) {
	first, err := Resolve(baseConfig(), quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := baseConfig()
	cfg.Accent = "#0f766e"
	second, err := Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if first.StylesheetURL == second.StylesheetURL {
		t.Error("different accents produced the same stylesheet URL")
	}
}

func TestAccentForegroundMeetsContrast(t *testing.T) {
	// A pale accent needs dark text; a deep one needs light text.
	for _, accent := range []string{"#ffe600", "#111827", "#4f46e5", "#0ea5e9"} {
		cfg := baseConfig()
		cfg.Accent = accent
		b, err := Resolve(cfg, quietLogger())
		if err != nil {
			t.Fatalf("Resolve(%s): %v", accent, err)
		}
		parsed, err := ParseHex(accent)
		if err != nil {
			t.Fatalf("ParseHex: %v", err)
		}
		// The foreground must pair with the accent the stylesheet actually
		// publishes, not with the operator's input: a dark accent is lightened
		// first, and that can flip which foreground wins.
		published := darkPalette(parsed, false).Accent
		fg, ratio := bestForeground(published)
		if ratio < Contrast(published, fgLight) && ratio < Contrast(published, fgDark) {
			t.Errorf("accent %s: chose the worse foreground", accent)
		}
		if !strings.Contains(string(b.Stylesheet()), fg.Hex()) {
			t.Errorf("accent %s: stylesheet does not use the chosen foreground %s", accent, fg.Hex())
		}
	}
}

// A low-contrast brand colour is the operator's decision, so it warns rather
// than blocking startup.
//
// The accent has to be supplied as an explicit dark accent to reach the
// warning. A derived one cannot: darkPalette lightens until the accent clears
// 4.5:1 against the dark surface, and fgDark is within a hair of that surface,
// so anything that survives the loop already has a legible foreground. Only an
// operator overriding the derivation can produce an illegible pair.
func TestLowContrastAccentWarnsButSucceeds(t *testing.T) {
	log, buf := capturingLogger()
	cfg := baseConfig()
	// A mid grey sitting in the narrow band where neither near-white nor
	// near-black text clears 4.5:1.
	cfg.AccentDark = "#797979"
	if _, err := Resolve(cfg, log); err != nil {
		t.Fatalf("Resolve should not fail on a low-contrast accent: %v", err)
	}
	if !strings.Contains(buf.String(), "low text contrast") {
		t.Errorf("expected a contrast warning, got:\n%s", buf.String())
	}
}

// The counterpart to the above: an accent left to the derivation never trips
// the warning, because the lightening loop guarantees a legible foreground.
// This pins that guarantee so a change to the loop cannot quietly remove it.
func TestDerivedAccentNeverWarns(t *testing.T) {
	for _, accent := range []string{"#797979", "#000000", "#101820", "#ffe600"} {
		log, buf := capturingLogger()
		cfg := baseConfig()
		cfg.Accent = accent
		if _, err := Resolve(cfg, log); err != nil {
			t.Fatalf("Resolve(%s): %v", accent, err)
		}
		if strings.Contains(buf.String(), "low text contrast") {
			t.Errorf("accent %s: derived palette warned about contrast:\n%s", accent, buf.String())
		}
	}
}

// Without an explicit dark accent, a dark brand colour must be lightened enough
// to be legible on a dark background.
func TestDarkSchemeAccentIsLegible(t *testing.T) {
	accent, err := ParseHex("#101820")
	if err != nil {
		t.Fatalf("ParseHex: %v", err)
	}
	p := darkPalette(accent, false)
	if got := Contrast(p.Accent, surfaceDark); got < minContrast {
		t.Errorf("derived dark accent contrast = %.2f, want at least %.2f", got, minContrast)
	}

	// An explicit dark accent is taken as given, even if it is unwise.
	explicit := darkPalette(accent, true)
	if explicit.Accent != accent {
		t.Errorf("explicit dark accent was modified: %s", explicit.Accent.Hex())
	}
}

func TestParseHex(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "#4f46e5", want: "#4f46e5"},
		{in: "4f46e5", want: "#4f46e5"},
		{in: "#abc", want: "#aabbcc"},
		{in: "  #4F46E5  ", want: "#4f46e5"},
		{in: "", wantErr: true},
		{in: "#12345", wantErr: true},
		{in: "#gggggg", wantErr: true},
		{in: "rgb(1,2,3)", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseHex(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseHex(%q) = %s, want error", tt.in, got.Hex())
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHex(%q): %v", tt.in, err)
			continue
		}
		if got.Hex() != tt.want {
			t.Errorf("ParseHex(%q) = %s, want %s", tt.in, got.Hex(), tt.want)
		}
	}
}

func TestLogoIsServedContentAddressed(t *testing.T) {
	cfg := baseConfig()
	cfg.LogoFile = writeTemp(t, "logo.png", pngBytes)

	b, err := Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !b.HasLogo {
		t.Fatal("HasLogo is false with a logo configured")
	}
	if !strings.HasSuffix(b.LogoURL, ".png") {
		t.Errorf("LogoURL = %q, want a .png extension", b.LogoURL)
	}

	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, b.LogoURL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", b.LogoURL, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable directive", rec.Header().Get("Cache-Control"))
	}
	if !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Error("served bytes differ from the source file")
	}
}

func TestSVGLogoIsSandboxed(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`)
	cfg := baseConfig()
	cfg.LogoFile = writeTemp(t, "logo.svg", svg)

	b, err := Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, b.LogoURL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", b.LogoURL, rec.Code)
	}
	// A direct navigation to the asset must not be able to run anything in our
	// origin, hence the sandbox and the locked-down policy.
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("SVG CSP = %q, want a sandboxed default-src 'none' policy", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestActiveSVGIsRejected(t *testing.T) {
	cases := map[string]string{
		"script element":  `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"event handler":   `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect/></svg>`,
		"foreign object":  `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body/></foreignObject></svg>`,
		"external href":   `<svg xmlns="http://www.w3.org/2000/svg"><image xlink:href="https://evil.example/x.png"/></svg>`,
		"entity":          `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"javascript href": `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"/></svg>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.LogoFile = writeTemp(t, "logo.svg", []byte(body))
			if _, err := Resolve(cfg, quietLogger()); err == nil {
				t.Error("Resolve accepted an SVG carrying active content")
			}
		})
	}
}

func TestUnsupportedImageIsRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.LogoFile = writeTemp(t, "logo.png", []byte("this is not an image"))
	if _, err := Resolve(cfg, quietLogger()); err == nil {
		t.Error("Resolve accepted a non-image file")
	}
}

func TestOversizedLogoIsRejected(t *testing.T) {
	oversized := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{0}, maxLogoBytes)...)
	cfg := baseConfig()
	cfg.LogoFile = writeTemp(t, "logo.png", oversized)
	if _, err := Resolve(cfg, quietLogger()); err == nil {
		t.Error("Resolve accepted a logo over the size limit")
	}
}

func TestMissingLogoFileFailsFast(t *testing.T) {
	cfg := baseConfig()
	cfg.LogoFile = filepath.Join(t.TempDir(), "absent.png")
	// A mounted-but-missing logo is a deployment mistake. Failing loudly beats
	// silently rendering a wordmark nobody expected.
	if _, err := Resolve(cfg, quietLogger()); err == nil {
		t.Error("Resolve accepted a missing logo file")
	}
}

func TestInvalidDisplayTextIsRejected(t *testing.T) {
	tests := map[string]func(*config.BrandConfig){
		"empty name":        func(c *config.BrandConfig) { c.Name = "  " },
		"control character": func(c *config.BrandConfig) { c.Name = "Bad\x00Name" },
		"newline":           func(c *config.BrandConfig) { c.Name = "Two\nLines" },
		"too long":          func(c *config.BrandConfig) { c.Name = strings.Repeat("a", 65) },
		"pre-escaped html":  func(c *config.BrandConfig) { c.Name = "Ben &amp; Co" },
		"bad accent":        func(c *config.BrandConfig) { c.Accent = "not-a-color" },
		"bad dark accent":   func(c *config.BrandConfig) { c.AccentDark = "#zzz" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			mutate(&cfg)
			if _, err := Resolve(cfg, quietLogger()); err == nil {
				t.Error("Resolve accepted invalid branding input")
			}
		})
	}
}

func TestUnknownAssetPathIs404(t *testing.T) {
	b, err := Resolve(baseConfig(), quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/brand/logo-deadbeef.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset = %d, want 404", rec.Code)
	}
}

// The organisation name is optional: a deployment that omits it must still
// start, and the sign-in page drops the "your X account" framing instead.
func TestOrgNameIsOptional(t *testing.T) {
	cfg := config.BrandConfig{
		Name:    "llm.example",
		Tagline: "Private endpoint",
		Accent:  "#3b6fd6",
	}
	b, err := Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve without an org name: %v", err)
	}
	if b.OrgName != "" {
		t.Errorf("OrgName = %q, want empty", b.OrgName)
	}

	cfg.OrgName = "  E-gineering  "
	b, err = Resolve(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.OrgName != "E-gineering" {
		t.Errorf("OrgName = %q, want it trimmed to E-gineering", b.OrgName)
	}
}
