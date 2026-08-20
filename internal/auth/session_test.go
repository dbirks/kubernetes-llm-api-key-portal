package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKeys(n int) [][]byte {
	keys := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		key := make([]byte, 32)
		for j := range key {
			key[j] = byte(i*31 + j)
		}
		keys = append(keys, key)
	}
	return keys
}

func newTestSealer(t *testing.T, secure bool, keys [][]byte) *Sealer {
	t.Helper()
	s, err := NewSealer(keys, secure)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

// roundTrip issues a session and returns a request carrying the resulting
// cookies, as a browser would.
func roundTrip(t *testing.T, s *Sealer, user SessionUser) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Issue(rec, user); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestSessionRoundTrip(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	want := SessionUser{
		TenantID: "tenant-1",
		ObjectID: "object-1",
		Name:     "Alice Example",
		Email:    "alice@example.com",
	}

	got, err := s.Read(roundTrip(t, s, want))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.TenantID != want.TenantID || got.ObjectID != want.ObjectID ||
		got.Name != want.Name || got.Email != want.Email {
		t.Errorf("Read returned %+v, want %+v", got, want)
	}
	if got.IssuedAt.IsZero() {
		t.Error("IssuedAt was not stamped")
	}
}

// The cookie must be opaque: a bearer of the cookie value alone should learn
// nothing about the user.
func TestSessionCookieIsEncrypted(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	rec := httptest.NewRecorder()
	if err := s.Issue(rec, SessionUser{
		TenantID: "tenant-1", ObjectID: "object-1",
		Name: "Alice Example", Email: "alice@example.com",
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cookie := rec.Result().Cookies()[0]
	for _, secret := range []string{"alice@example.com", "Alice Example", "tenant-1", "object-1"} {
		if strings.Contains(cookie.Value, secret) {
			t.Errorf("cookie value leaks %q", secret)
		}
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	t.Run("https deployment", func(t *testing.T) {
		s := newTestSealer(t, true, testKeys(1))
		rec := httptest.NewRecorder()
		if err := s.Issue(rec, SessionUser{TenantID: "t", ObjectID: "o"}); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		c := rec.Result().Cookies()[0]

		if !c.Secure {
			t.Error("cookie is not Secure")
		}
		if !c.HttpOnly {
			t.Error("cookie is not HttpOnly")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite = %v, want Lax (Strict would drop the OIDC callback)", c.SameSite)
		}
		if !strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("cookie name = %q, want a __Host- prefix", c.Name)
		}
		if c.Path != "/" {
			t.Errorf("Path = %q, want / as __Host- requires", c.Path)
		}
	})

	// Browsers reject __Host- without Secure, which would break plain-http
	// loopback development entirely.
	t.Run("loopback development", func(t *testing.T) {
		s := newTestSealer(t, false, testKeys(1))
		rec := httptest.NewRecorder()
		if err := s.Issue(rec, SessionUser{TenantID: "t", ObjectID: "o"}); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		c := rec.Result().Cookies()[0]
		if c.Secure {
			t.Error("cookie is Secure on a plain-http origin")
		}
		if strings.HasPrefix(c.Name, "__Host-") {
			t.Error("cookie uses a __Host- name without Secure; browsers would reject it")
		}
		if !c.HttpOnly {
			t.Error("cookie is not HttpOnly")
		}
	})
}

func TestTamperedCookieIsRejected(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	req := roundTrip(t, s, SessionUser{TenantID: "t", ObjectID: "o"})

	original := req.Cookies()[0]

	// flip mutates one byte of the decoded payload and re-encodes it. Decoding
	// first is deliberate: RawURLEncoding is non-canonical, so flipping the last
	// base64 *character* may only touch trailing bits the decoder discards,
	// leaving the ciphertext — and the GCM tag — unchanged. Mutating a decoded
	// byte is what actually guarantees the seal no longer verifies.
	raw, err := base64.RawURLEncoding.DecodeString(original.Value)
	if err != nil {
		t.Fatalf("sealed cookie is not valid base64: %v", err)
	}
	flip := func(i int) string {
		b := append([]byte(nil), raw...)
		b[i] ^= 0xff
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tampered := []string{
		flip(len(raw) - 1),                     // last byte of the tag
		flip(0),                                // first byte of the nonce
		flip(len(raw) / 2),                     // middle of the ciphertext
		original.Value[:len(original.Value)/2], // truncated
		"not-base64-at-all!!",
		"",
	}
	for _, value := range tampered {
		bad := httptest.NewRequest(http.MethodGet, "/", nil)
		bad.AddCookie(&http.Cookie{Name: original.Name, Value: value})
		if _, err := s.Read(bad); err == nil {
			t.Errorf("Read accepted a tampered cookie value %q", value)
		}
	}
}

// A cookie sealed with one deployment's key must be worthless to another.
func TestCookieFromAnotherKeyIsRejected(t *testing.T) {
	keys := testKeys(2)
	issuer := newTestSealer(t, true, keys[:1])
	other := newTestSealer(t, true, keys[1:])

	req := roundTrip(t, issuer, SessionUser{TenantID: "t", ObjectID: "o"})
	if _, err := other.Read(req); err == nil {
		t.Error("a session sealed with a different key was accepted")
	}
}

// Rotation: a new key is prepended, and sessions sealed with the old key keep
// working until they expire.
func TestKeyRotationAcceptsOldSessions(t *testing.T) {
	keys := testKeys(2)
	oldSealer := newTestSealer(t, true, keys[1:])
	req := roundTrip(t, oldSealer, SessionUser{TenantID: "t", ObjectID: "o"})

	rotated := newTestSealer(t, true, [][]byte{keys[0], keys[1]})
	if _, err := rotated.Read(req); err != nil {
		t.Errorf("session sealed with the retired key was rejected: %v", err)
	}

	// And new sessions seal with the primary key only.
	fresh := roundTrip(t, rotated, SessionUser{TenantID: "t", ObjectID: "o"})
	if _, err := oldSealer.Read(fresh); err == nil {
		t.Error("a session sealed with the new key was readable by the retired key alone")
	}
}

// The lifetime is enforced from the sealed payload, not the cookie's Max-Age,
// which a client can simply ignore.
func TestExpiredSessionIsRejected(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	expired := SessionUser{
		TenantID: "t", ObjectID: "o",
		IssuedAt: time.Now().Add(-SessionTTL - time.Minute),
	}
	payload := mustSeal(t, s, expired)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: s.name(sessionCookieName, sessionCookieNameDev), Value: payload})
	if _, err := s.Read(req); err == nil {
		t.Error("an expired session was accepted")
	}
}

// A session without both halves of the identity must never be usable, since it
// could not be authorized against anything.
func TestSessionWithoutIdentityIsRejected(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	for _, user := range []SessionUser{
		{ObjectID: "o", IssuedAt: time.Now()},
		{TenantID: "t", IssuedAt: time.Now()},
		{IssuedAt: time.Now()},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  s.name(sessionCookieName, sessionCookieNameDev),
			Value: mustSeal(t, s, user),
		})
		if _, err := s.Read(req); err == nil {
			t.Errorf("accepted a session with incomplete identity %+v", user)
		}
	}
}

func TestClearExpiresTheCookie(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))
	rec := httptest.NewRecorder()
	s.Clear(rec)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Clear set %d cookies, want 1", len(cookies))
	}
	if cookies[0].MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative value to delete the cookie", cookies[0].MaxAge)
	}
	if cookies[0].Value != "" {
		t.Errorf("Value = %q, want empty", cookies[0].Value)
	}
}

func TestSealerRequiresAKey(t *testing.T) {
	if _, err := NewSealer(nil, true); err == nil {
		t.Error("NewSealer accepted an empty key list")
	}
}

func TestFlashIsOneShot(t *testing.T) {
	s := newTestSealer(t, true, testKeys(1))

	rec := httptest.NewRecorder()
	s.SetFlash(rec, FlashSuccess, "API key revoked.")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	readRec := httptest.NewRecorder()
	flashes := s.TakeFlash(readRec, req)
	if len(flashes) != 1 || flashes[0].Message != "API key revoked." {
		t.Fatalf("TakeFlash returned %+v, want one message", flashes)
	}
	// Reading must also clear it, so the message does not follow the user
	// around.
	cleared := readRec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Error("TakeFlash did not expire the flash cookie")
	}
}

func mustSeal(t *testing.T, s *Sealer, user SessionUser) string {
	t.Helper()
	payload, err := marshalUser(user)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	value, err := s.seal(payload)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return value
}
