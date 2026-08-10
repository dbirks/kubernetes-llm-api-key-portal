package avatar

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pngBytes builds a real PNG so that the decode check under test sees genuine
// image data rather than a hand-written magic number.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// graphStub serves the photo endpoints, recording what was asked for.
type graphStub struct {
	*httptest.Server
	paths []string
	auth  []string
}

// newStub redirects the package's endpoint list at a test server for the
// duration of one test.
func newStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *graphStub {
	t.Helper()
	stub := &graphStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.paths = append(stub.paths, r.URL.Path)
		stub.auth = append(stub.auth, r.Header.Get("Authorization"))
		handler(w, r)
	}))
	t.Cleanup(stub.Close)

	original := photoEndpoints
	photoEndpoints = []string{
		stub.URL + "/v1.0/me/photos/96x96/$value",
		stub.URL + "/v1.0/me/photos/48x48/$value",
		stub.URL + "/v1.0/me/photo/$value",
	}
	t.Cleanup(func() { photoEndpoints = original })
	return stub
}

func TestCaptureStoresPhoto(t *testing.T) {
	want := pngBytes(t, 96, 96)
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(want)
	})

	s := New(Options{})
	s.Capture(context.Background(), "tid:oid", "token")

	got, ok := s.Get("tid:oid")
	if !ok {
		t.Fatal("photo was not cached")
	}
	if !bytes.Equal(got.Bytes, want) {
		t.Error("cached bytes differ from what Graph returned")
	}
	if got.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", got.ContentType)
	}
	if got.Version == "" {
		t.Error("Version is empty, so the URL could not be cache-busted")
	}
}

// The content type must come from the bytes, not from Graph's header, or a
// wrong header would be echoed straight back to the browser.
func TestContentTypeIsDerivedFromTheBytes(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(jpegBytes(t, 48, 48))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	got, ok := s.Get("k")
	if !ok {
		t.Fatal("photo was not cached")
	}
	if got.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg regardless of the upstream header", got.ContentType)
	}
}

func TestSizedEndpointsAreTriedBeforeTheUnsizedOne(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		// Only the unsized endpoint has a photo, as for an Entra-only user.
		if strings.HasSuffix(r.URL.Path, "/me/photo/$value") {
			w.Write(pngBytes(t, 64, 64))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); !ok {
		t.Fatal("photo was not cached; the unsized fallback was not reached")
	}
	if len(stub.paths) != 3 {
		t.Errorf("tried %d endpoints (%v), want all 3", len(stub.paths), stub.paths)
	}
}

func TestTokenIsSentAsBearer(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 48, 48))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "secret-token")

	if len(stub.auth) == 0 || stub.auth[0] != "Bearer secret-token" {
		t.Errorf("Authorization = %v, want a Bearer header", stub.auth)
	}
}

// A user with no photo is the ordinary case, and must leave the cache empty
// rather than storing an empty entry that renders as a broken image.
func TestNoPhotoCachesNothing(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); ok {
		t.Error("cached something for a user with no photo")
	}
}

// A refusal must stop the walk: retrying every size cannot help, and each
// attempt is another request on the sign-in critical path.
func TestRefusalStopsAfterOneRequest(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); ok {
		t.Error("cached a photo despite a 403")
	}
	if len(stub.paths) != 1 {
		t.Errorf("made %d requests, want 1 after a refusal", len(stub.paths))
	}
}

func TestNonImageBodyIsRejected(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("<html>this is not an image</html>"))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); ok {
		t.Error("cached a body that is not a decodable image")
	}
}

func TestOversizedPhotoIsRejected(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		// A valid PNG header followed by more than the cap allows.
		w.Write(append(pngBytes(t, 8, 8), bytes.Repeat([]byte{0}, maxPhotoBytes+1)...))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); ok {
		t.Errorf("cached a photo larger than the %d byte cap", maxPhotoBytes)
	}
}

// A small file that decodes to an enormous bitmap is bounded by pixels, not
// bytes: the byte cap does not limit what the browser would allocate.
func TestOversizedDimensionsAreRejected(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, maxPhotoPixels+1, 8))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")

	if _, ok := s.Get("k"); ok {
		t.Error("cached a photo wider than the pixel cap")
	}
}

func TestExpiredPhotoIsNotServed(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 48, 48))
	})

	s := New(Options{TTL: time.Nanosecond})
	s.Capture(context.Background(), "k", "token")
	time.Sleep(time.Millisecond)

	if _, ok := s.Get("k"); ok {
		t.Error("served a photo past its TTL")
	}
}

func TestCacheIsBounded(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 8, 8))
	})

	s := New(Options{MaxEntries: 2})
	for _, key := range []string{"a", "b", "c"} {
		s.Capture(context.Background(), key, "token")
	}

	if _, ok := s.Get("a"); ok {
		t.Error("oldest entry survived past the cache bound")
	}
	if _, ok := s.Get("c"); !ok {
		t.Error("newest entry was evicted")
	}
	if len(s.entries) > 2 {
		t.Errorf("cache holds %d entries, want at most 2", len(s.entries))
	}
}

// Re-capturing an existing key must not consume a second eviction slot.
func TestRecaptureDoesNotGrowTheCache(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 8, 8))
	})

	s := New(Options{MaxEntries: 2})
	s.Capture(context.Background(), "a", "token")
	s.Capture(context.Background(), "a", "token")
	s.Capture(context.Background(), "b", "token")

	if _, ok := s.Get("a"); !ok {
		t.Error("re-capturing a key evicted it")
	}
	if _, ok := s.Get("b"); !ok {
		t.Error("second key was evicted")
	}
}

// A nil *Store is how avatars are disabled, so every method must tolerate it.
func TestNilStoreIsUsable(t *testing.T) {
	var s *Store
	s.Capture(context.Background(), "k", "token")
	if _, ok := s.Get("k"); ok {
		t.Error("a nil store returned a photo")
	}
}

func TestCaptureIgnoresEmptyKeyOrToken(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 8, 8))
	})

	s := New(Options{})
	s.Capture(context.Background(), "", "token")
	s.Capture(context.Background(), "k", "")

	if len(stub.paths) != 0 {
		t.Errorf("made %d requests without a key or token, want 0", len(stub.paths))
	}
}

// Identical bytes must produce identical versions, and different bytes must
// not, or the cache-busting query parameter would be meaningless.
func TestVersionTracksContent(t *testing.T) {
	first := pngBytes(t, 16, 16)
	second := pngBytes(t, 32, 32)
	body := first
	newStub(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")
	a, _ := s.Get("k")

	s.Capture(context.Background(), "k2", "token")
	b, _ := s.Get("k2")
	if a.Version != b.Version {
		t.Error("identical bytes produced different versions")
	}

	body = second
	s.Capture(context.Background(), "k3", "token")
	c, _ := s.Get("k3")
	if a.Version == c.Version {
		t.Error("different bytes produced the same version")
	}
}

func TestForgetRemovesAPhoto(t *testing.T) {
	newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, 48, 48))
	})

	s := New(Options{})
	s.Capture(context.Background(), "k", "token")
	if _, ok := s.Get("k"); !ok {
		t.Fatal("photo was not cached")
	}

	s.Forget("k")
	if _, ok := s.Get("k"); ok {
		t.Error("photo survived Forget")
	}

	// Must be safe on a nil store and an absent key.
	var nilStore *Store
	nilStore.Forget("k")
	s.Forget("never-cached")
}
