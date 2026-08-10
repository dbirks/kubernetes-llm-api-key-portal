// Package avatar fetches and caches Microsoft Graph profile photos.
//
// An avatar is decoration. Nothing the portal does depends on having one, so
// every failure path here ends in "no photo" rather than an error the caller
// has to handle: sign-in must not become fragile because a photo endpoint is
// slow, unlicensed, or absent. Users without a photo fall back to initials,
// which is also what every user gets when avatars are switched off.
//
// The cache is per-process and in memory. That is a deliberate limit rather
// than an oversight: the photo is fetched once, during the OIDC callback, with
// the access token from that exchange, and the token is then discarded. Keeping
// it to serve later cache misses would mean persisting a Graph-capable bearer
// token, which is a much worse trade than occasionally showing initials. With
// more than one replica a user who signs in on one pod and is later served by
// another sees initials there until their next sign-in.
package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	// Registered for image.DecodeConfig, which is used to confirm that what
	// Graph returned really is an image before it is cached and served.
	_ "image/jpeg"
	_ "image/png"
)

// photoEndpoints are tried in order, smallest useful size first.
//
// Sized variants exist only for photos stored in Exchange Online. A user whose
// photo lives in Entra ID alone has just the unsized endpoint, which 404s on
// every /photos/{size}/ request, so the list ends with it as the fallback
// rather than starting with it as the preference.
var photoEndpoints = []string{
	"https://graph.microsoft.com/v1.0/me/photos/96x96/$value",
	"https://graph.microsoft.com/v1.0/me/photos/48x48/$value",
	"https://graph.microsoft.com/v1.0/me/photo/$value",
}

const (
	// maxPhotoBytes bounds what is read from Graph and held per user. Sized
	// variants are a few kilobytes; the unsized endpoint returns the original
	// upload, which can be far larger.
	maxPhotoBytes = 1 << 20

	// maxPhotoPixels rejects a small file that decodes to a huge bitmap. The
	// byte cap alone does not bound what the browser allocates.
	maxPhotoPixels = 2048

	// fetchTimeout bounds the added latency of the sign-in callback. Graph is
	// not on the critical path of authentication, so it does not get to hold
	// the redirect open.
	fetchTimeout = 5 * time.Second

	defaultTTL        = 12 * time.Hour
	defaultMaxEntries = 512
)

// errNoPhoto means the user has no photo at this endpoint. It is the ordinary
// case, not a fault, and is logged at debug.
var errNoPhoto = errors.New("no photo")

// errNotPermitted means Graph refused the token. Unlike a missing photo this is
// a misconfiguration worth surfacing, so it is logged at warn and stops the
// endpoint walk rather than trying the next size.
var errNotPermitted = errors.New("not permitted")

// Photo is a cached profile photo, ready to serve.
type Photo struct {
	Bytes       []byte
	ContentType string

	// Version is a URL-safe digest of Bytes. It is both the cache-busting
	// query parameter and the entity tag, so a changed photo produces a new
	// URL instead of waiting out a max-age somewhere.
	Version string
}

type entry struct {
	photo    Photo
	storedAt time.Time
}

// Options configures a Store. The zero value is usable.
type Options struct {
	Log        *slog.Logger
	HTTPClient *http.Client
	TTL        time.Duration
	MaxEntries int
}

// Store caches profile photos keyed by an opaque per-user string.
type Store struct {
	log    *slog.Logger
	client *http.Client
	ttl    time.Duration
	max    int

	mu      sync.Mutex
	entries map[string]entry
	// order holds keys oldest-first, for eviction. A photo cache does not
	// benefit enough from recency tracking to justify an LRU: entries expire
	// on their own, and evicting the oldest costs one extra Graph call at that
	// user's next sign-in.
	order []string
}

// New returns a Store. A nil *Store is safe to use and caches nothing, which
// is how avatars are disabled.
func New(opts Options) *Store {
	s := &Store{
		log:     opts.Log,
		client:  opts.HTTPClient,
		ttl:     opts.TTL,
		max:     opts.MaxEntries,
		entries: make(map[string]entry),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: fetchTimeout}
	}
	if s.ttl <= 0 {
		s.ttl = defaultTTL
	}
	if s.max <= 0 {
		s.max = defaultMaxEntries
	}
	return s
}

// Capture fetches the signed-in user's photo and caches it under key.
//
// It never returns an error. The access token is used for the duration of the
// call and not retained.
func (s *Store) Capture(ctx context.Context, key, accessToken string) {
	if s == nil || key == "" || accessToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	photo, err := s.fetch(ctx, accessToken)
	switch {
	case errors.Is(err, errNoPhoto):
		s.log.Debug("user has no profile photo; falling back to initials")
		return
	case errors.Is(err, errNotPermitted):
		// Almost always a missing User.Read delegated permission, or a tenant
		// policy blocking photo reads. Worth a warning because every user's
		// avatar is silently absent until it is fixed.
		s.log.Warn("microsoft graph refused the profile photo request; check the User.Read delegated permission",
			"error", err)
		return
	case err != nil:
		s.log.Warn("could not fetch profile photo", "error", err)
		return
	}
	s.put(key, photo)
}

// Get returns a cached photo if one is present and unexpired.
func (s *Store) Get(key string) (Photo, bool) {
	if s == nil || key == "" {
		return Photo{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	if !ok {
		return Photo{}, false
	}
	if time.Since(e.storedAt) > s.ttl {
		delete(s.entries, key)
		return Photo{}, false
	}
	return e.photo, true
}

// Forget drops a cached photo, so that signing out stops the portal from
// holding a picture of someone who has left. It is not a security boundary —
// only that user's own session could ever have reached the photo — but the data
// has no purpose once the session is gone.
func (s *Store) Forget(key string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

func (s *Store) put(key string, photo Photo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[key]; !exists {
		s.order = append(s.order, key)
	}
	s.entries[key] = entry{photo: photo, storedAt: time.Now()}

	// Evict oldest-first until back within bounds. A key that was already
	// deleted by expiry leaves a stale entry in order, so this skips those
	// rather than counting them as evictions.
	for len(s.entries) > s.max && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
}

// fetch walks the endpoints and returns the first photo found.
func (s *Store) fetch(ctx context.Context, accessToken string) (Photo, error) {
	for _, url := range photoEndpoints {
		photo, err := s.get(ctx, url, accessToken)
		if err == nil {
			return photo, nil
		}
		if !errors.Is(err, errNoPhoto) {
			// A refusal or a malformed response will repeat at every size.
			return Photo{}, err
		}
	}
	return Photo{}, errNoPhoto
}

func (s *Store) get(ctx context.Context, url, accessToken string) (Photo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Photo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		// The URL is a constant and carries no secret, but the error can quote
		// the request, so it is summarised rather than wrapped verbatim.
		return Photo{}, errors.New("graph request failed")
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Photo{}, errNoPhoto
	case http.StatusUnauthorized, http.StatusForbidden:
		return Photo{}, fmt.Errorf("%w: graph returned %s", errNotPermitted, resp.Status)
	default:
		return Photo{}, fmt.Errorf("graph returned %s", resp.Status)
	}

	// One byte past the cap distinguishes "exactly at the limit" from
	// "truncated", so an oversized photo is dropped rather than served as a
	// corrupt image.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return Photo{}, errors.New("reading photo body failed")
	}
	if len(body) > maxPhotoBytes {
		return Photo{}, fmt.Errorf("photo is larger than %d bytes", maxPhotoBytes)
	}

	// Decode the header only. This is what makes it safe to serve the bytes
	// back with an image content type later: the type is derived from the
	// content, never from what Graph claimed in its own Content-Type.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return Photo{}, fmt.Errorf("photo is not a decodable image: %w", err)
	}
	if format != "jpeg" && format != "png" {
		return Photo{}, fmt.Errorf("photo is %s, want jpeg or png", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxPhotoPixels || cfg.Height > maxPhotoPixels {
		return Photo{}, fmt.Errorf("photo is %dx%d, outside the accepted range", cfg.Width, cfg.Height)
	}

	sum := sha256.Sum256(body)
	return Photo{
		Bytes:       body,
		ContentType: "image/" + format,
		Version:     base64.RawURLEncoding.EncodeToString(sum[:12]),
	}, nil
}
