package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Session cookie names. The __Host- prefix binds a cookie to the exact origin
// and requires Secure + Path=/, which browsers enforce for us. It is
// incompatible with plain http, so loopback development falls back to a plain
// name.
const (
	sessionCookieName       = "__Host-session"
	sessionCookieNameDev    = "portal_session"
	oauthStateCookieName    = "__Host-oauth"
	oauthStateCookieNameDev = "portal_oauth"
	flashCookieName         = "__Host-flash"
	flashCookieNameDev      = "portal_flash"
)

// SessionTTL is how long a sign-in lasts. Roughly one workday, so a user signs
// in about once a day rather than on every visit.
const SessionTTL = 10 * time.Hour

// ErrNoSession means there was no usable session cookie. It is an ordinary
// condition, not a failure.
var ErrNoSession = errors.New("no session")

// SessionUser is the entire contents of a session.
//
// It holds identity only. Credentials are never stored here: the portal cannot
// read back an API key, and a session cookie is the last place one should live.
type SessionUser struct {
	TenantID string `json:"tid"`
	ObjectID string `json:"oid"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`

	// IssuedAt bounds the session independently of the cookie expiry, which a
	// client controls and can extend.
	IssuedAt time.Time `json:"iat"`
}

// Sealer encrypts and authenticates session payloads.
//
// Keys are used in order: the first seals, and every key is tried when opening.
// Rotation is therefore a matter of prepending a new key, deploying, and
// dropping the old one once existing sessions have expired.
type Sealer struct {
	aeads  []cipher.AEAD
	secure bool
}

// NewSealer derives AEADs from the configured session keys.
//
// secure selects cookie attributes: true issues __Host- prefixed Secure
// cookies, which is correct everywhere except plain-http loopback development.
func NewSealer(keys [][]byte, secure bool) (*Sealer, error) {
	if len(keys) == 0 {
		return nil, errors.New("at least one session key is required")
	}
	s := &Sealer{secure: secure}
	for i, key := range keys {
		// Derive a dedicated encryption key rather than using the configured
		// bytes directly, so the same secret could safely be reused for another
		// purpose under a different info string.
		derived, err := hkdf.Key(sha256.New, key, nil, "ai-account session v1", 32)
		if err != nil {
			return nil, fmt.Errorf("derive session key %d: %w", i+1, err)
		}
		block, err := aes.NewCipher(derived)
		if err != nil {
			return nil, fmt.Errorf("session key %d: %w", i+1, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("session key %d: %w", i+1, err)
		}
		s.aeads = append(s.aeads, aead)
	}
	return s, nil
}

// seal encrypts a payload with the primary key. The nonce is prepended to the
// ciphertext, which is the conventional layout for GCM at rest.
func (s *Sealer) seal(payload []byte) (string, error) {
	aead := s.aeads[0]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("session nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, payload, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open decrypts a payload, trying each key in turn to support rotation.
func (s *Sealer) open(value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrNoSession
	}
	for _, aead := range s.aeads {
		if len(raw) < aead.NonceSize() {
			continue
		}
		nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
		if plaintext, err := aead.Open(nil, nonce, ciphertext, nil); err == nil {
			return plaintext, nil
		}
	}
	return nil, ErrNoSession
}

// Issue writes a session cookie for the given user.
func (s *Sealer) Issue(w http.ResponseWriter, user SessionUser) error {
	user.IssuedAt = time.Now().UTC()
	payload, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	value, err := s.seal(payload)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.cookie(s.name(sessionCookieName, sessionCookieNameDev), value, SessionTTL))
	return nil
}

// Read returns the session carried by the request, or ErrNoSession.
func (s *Sealer) Read(r *http.Request) (SessionUser, error) {
	c, err := r.Cookie(s.name(sessionCookieName, sessionCookieNameDev))
	if err != nil {
		return SessionUser{}, ErrNoSession
	}
	payload, err := s.open(c.Value)
	if err != nil {
		return SessionUser{}, ErrNoSession
	}
	var user SessionUser
	if err := json.Unmarshal(payload, &user); err != nil {
		return SessionUser{}, ErrNoSession
	}
	// Enforce the lifetime server-side. The cookie's own Max-Age is a hint the
	// client is free to ignore; this is the check that actually matters.
	if time.Since(user.IssuedAt) > SessionTTL {
		return SessionUser{}, ErrNoSession
	}
	if user.TenantID == "" || user.ObjectID == "" {
		return SessionUser{}, ErrNoSession
	}
	return user, nil
}

// Clear expires the session cookie.
func (s *Sealer) Clear(w http.ResponseWriter) {
	s.expire(w, s.name(sessionCookieName, sessionCookieNameDev))
}

// cookie builds a cookie with the security attributes this app always wants.
func (s *Sealer) cookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:  name,
		Value: value,
		Path:  "/",
		// Lax rather than Strict: the OIDC callback is a cross-site top-level
		// navigation back from Microsoft, and Strict would drop the cookie on
		// arrival.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   s.secure,
		MaxAge:   int(ttl.Seconds()),
	}
}

func (s *Sealer) expire(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   s.secure,
		MaxAge:   -1,
	})
}

// name picks the __Host- prefixed cookie name when cookies are Secure, and the
// plain name otherwise. Browsers reject __Host- cookies without Secure, so this
// keeps loopback development working without weakening production.
func (s *Sealer) name(secureName, devName string) string {
	if s.secure {
		return secureName
	}
	return devName
}

// marshalUser encodes a session payload. Exposed to package tests so they can
// construct sealed cookies that Issue would never produce, such as an expired
// or identity-less session.
func marshalUser(user SessionUser) ([]byte, error) { return json.Marshal(user) }
