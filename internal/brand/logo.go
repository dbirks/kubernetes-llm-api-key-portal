package brand

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// maxLogoBytes caps what an operator can mount as a logo. Anything larger is
// almost certainly a mistake, and the file is held in memory for the process
// lifetime.
const maxLogoBytes = 512 << 10 // 512 KiB

// defaultLogoSVG is the built-in e-gineering mark, compiled into the binary and
// served whenever no BRAND_LOGO_FILE is mounted. It ships branded out of the
// box: no ConfigMap, no volume, nothing to wire up at deploy time. It is a
// flattened single-path SVG that clears checkSVG, and it is validated and
// content-addressed through the very same path as an operator-mounted file.
//
//go:embed egineering.svg
var defaultLogoSVG []byte

// Asset is an operator-supplied image served from our own origin.
type Asset struct {
	// URLPath includes a content hash, so swapping the file invalidates caches
	// without any cache-control gymnastics.
	URLPath     string
	ContentType string
	Bytes       []byte
}

var allowedImageTypes = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
}

// svgHazards are constructs that make an SVG active content rather than an
// image. They are inert inside an <img> tag, but the asset is also reachable by
// direct navigation, where they would execute in our origin.
var svgHazards = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<\s*script`),
	regexp.MustCompile(`(?i)<\s*foreignObject`),
	regexp.MustCompile(`(?i)<\s*(iframe|embed|object)\b`),
	regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
	regexp.MustCompile(`(?i)(xlink:)?href\s*=\s*["']?\s*(https?:|//|javascript:|data:)`),
	regexp.MustCompile(`(?i)<!ENTITY`),
}

// loadAsset reads an operator-supplied image file, validates it, and prepares
// it for serving under a content-addressed path.
func loadAsset(path, urlPrefix string) (*Asset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return buildAsset(data, path, urlPrefix)
}

// buildAsset validates raw image bytes from any source — a mounted file or the
// embedded default — and prepares them for content-addressed serving. source is
// a human-readable label used only in error messages.
func buildAsset(data []byte, source, urlPrefix string) (*Asset, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%s is empty", source)
	}
	if len(data) > maxLogoBytes {
		return nil, fmt.Errorf("%s is %d bytes, limit is %d", source, len(data), maxLogoBytes)
	}

	contentType := detectImageType(data)
	ext, ok := allowedImageTypes[contentType]
	if !ok {
		return nil, fmt.Errorf("%s: unsupported image type %q (allowed: PNG, JPEG, WebP, SVG)", source, contentType)
	}
	if contentType == "image/svg+xml" {
		if err := checkSVG(data); err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
	}

	sum := sha256.Sum256(data)
	return &Asset{
		URLPath:     urlPrefix + hex.EncodeToString(sum[:])[:12] + ext,
		ContentType: contentType,
		Bytes:       data,
	}, nil
}

// detectImageType sniffs the content rather than trusting the filename, so a
// mislabelled .png cannot smuggle in another format.
func detectImageType(data []byte) string {
	// http.DetectContentType does not recognise SVG or WebP, so check those first.
	if isSVG(data) {
		return "image/svg+xml"
	}
	if len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

func isSVG(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	lower := strings.ToLower(string(head))
	return strings.Contains(lower, "<svg")
}

func checkSVG(data []byte) error {
	for _, hazard := range svgHazards {
		if hazard.Match(data) {
			return fmt.Errorf("SVG contains active content (%s); export a flattened SVG or use a PNG", hazard.String())
		}
	}
	return nil
}

// ServeAsset writes the asset with immutable caching. SVGs additionally get a
// restrictive CSP and sandbox so a direct navigation to the URL cannot become a
// script execution vector in our origin.
func ServeAsset(w http.ResponseWriter, a *Asset) {
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	if a.ContentType == "image/svg+xml" {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	w.Write(a.Bytes)
}
