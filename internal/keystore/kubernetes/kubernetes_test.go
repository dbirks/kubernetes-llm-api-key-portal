package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	ks "github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

const (
	testNamespace  = "llm-portal"
	testSecretName = "llm-apikeys"
)

var (
	alice = ks.Owner{
		TenantID: "11111111-1111-1111-1111-111111111111",
		ObjectID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",

		DisplayName: "Alice Example",
		Email:       "alice@example.com",
	}
	bob = ks.Owner{
		TenantID: "11111111-1111-1111-1111-111111111111",
		ObjectID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	}
)

func newTestStore(t *testing.T, objects ...runtime.Object) (*Store, *fake.Clientset) {
	t.Helper()
	client := fake.NewClientset(objects...)
	store, err := New(Options{
		Client:     client,
		Namespace:  testNamespace,
		SecretName: testSecretName,
		KeyPrefix:  "llm_",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, client
}

// bootstrapSecret is the SOPS-managed aggregate Secret as it exists in the
// cluster before the portal ever runs: one bare test entry, no ownership
// annotations.
func bootstrapSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"client-test": []byte("llm_testkey123")},
	}
}

func readSecret(t *testing.T, client *fake.Clientset) *corev1.Secret {
	t.Helper()
	secret, err := client.CoreV1().Secrets(testNamespace).Get(context.Background(), testSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading aggregate secret: %v", err)
	}
	return secret
}

// TestCreateAddsEntryToAggregateSecret pins the exact shape the gateway reads:
// one data entry per key, `client-<id>: <bare credential>`, in the single
// aggregate Opaque Secret — no per-key Secret, no "Bearer " prefix.
func TestCreateAddsEntryToAggregateSecret(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t, bootstrapSecret())

	created, err := store.CreateKey(ctx, alice, "MacBook Claude Code")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	secret := readSecret(t, client)

	if got, want := string(secret.Type), string(corev1.SecretTypeOpaque); got != want {
		t.Errorf("Secret type = %q, want %q", got, want)
	}
	// The bootstrap entry must survive an unrelated issue.
	if string(secret.Data["client-test"]) != "llm_testkey123" {
		t.Errorf("bootstrap entry client-test was disturbed: %q", secret.Data["client-test"])
	}

	dataKey := "client-" + created.ID
	value, ok := secret.Data[dataKey]
	if !ok {
		t.Fatalf("no data entry %q in aggregate secret; have %v", dataKey, keysOf(secret.Data))
	}
	if string(value) != created.Secret {
		t.Errorf("stored value %q != returned credential %q", value, created.Secret)
	}
	if strings.HasPrefix(string(value), "Bearer ") {
		t.Error("stored credential carries a Bearer prefix; the gateway compares the bare token")
	}

	// Ownership + display metadata live in a per-key annotation, not a label.
	raw, ok := secret.Annotations["llm-portal/key-"+created.ID]
	if !ok {
		t.Fatalf("no ownership annotation for the created key")
	}
	var m keyMeta
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("annotation is not valid JSON: %v", err)
	}
	if m.TenantID != alice.TenantID || m.ObjectID != alice.ObjectID {
		t.Errorf("ownership tuple = %s/%s, want %s/%s", m.TenantID, m.ObjectID, alice.TenantID, alice.ObjectID)
	}
	if m.Name != "MacBook Claude Code" {
		t.Errorf("stored name = %q", m.Name)
	}
	if m.Suffix != created.Suffix {
		t.Errorf("stored suffix = %q, want %q", m.Suffix, created.Suffix)
	}
	if m.CreatedAt.IsZero() {
		t.Error("CreatedAt was not recorded")
	}
}

// The data key (client identifier, visible in gateway logs) must not carry the
// user's identity.
func TestClientIDCarriesNoIdentity(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t, bootstrapSecret())

	created, err := store.CreateKey(ctx, alice, "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	secret := readSecret(t, client)

	dataKey := "client-" + created.ID
	if _, ok := secret.Data[dataKey]; !ok {
		t.Fatalf("expected data entry %q", dataKey)
	}
	for _, forbidden := range []string{alice.Email, alice.ObjectID, alice.DisplayName, "alice"} {
		if strings.Contains(strings.ToLower(dataKey), strings.ToLower(forbidden)) {
			t.Errorf("client identifier %q leaks %q", dataKey, forbidden)
		}
	}
}

// CreateKey must bootstrap the aggregate Secret when it does not yet exist.
func TestCreateBootstrapsMissingSecret(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t) // no bootstrap object

	created, err := store.CreateKey(ctx, alice, "first key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	secret := readSecret(t, client)
	if string(secret.Type) != string(corev1.SecretTypeOpaque) {
		t.Errorf("created secret type = %q, want Opaque", secret.Type)
	}
	if _, ok := secret.Data["client-"+created.ID]; !ok {
		t.Errorf("created secret is missing the first key entry")
	}
}

func TestListIsScopedToOwner(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t, bootstrapSecret())

	if _, err := store.CreateKey(ctx, alice, "Alice key"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if _, err := store.CreateKey(ctx, bob, "Bob key"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Name != "Alice key" {
		t.Fatalf("ListKeys returned %+v, want only Alice's key", keys)
	}
}

// The bootstrap `client-test` entry has no ownership annotation, so it must not
// surface for any signed-in user.
func TestBootstrapEntryIsNotOwnedByAnyone(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t, bootstrapSecret())

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("ListKeys returned %d keys, want 0", len(keys))
	}
}

func TestAnotherUserCannotRevoke(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t, bootstrapSecret())

	created, err := store.CreateKey(ctx, alice, "Alice key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := store.RevokeKey(ctx, bob.ID(), created.ID); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("RevokeKey by another user = %v, want ErrNotFound", err)
	}
	secret := readSecret(t, client)
	if _, ok := secret.Data["client-"+created.ID]; !ok {
		t.Error("a user who does not own the key was able to remove its entry")
	}
}

// A key ID arrives from the URL, so it must be rejected before it can be
// interpolated into a data key or annotation name.
func TestMalformedKeyIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t, bootstrapSecret())

	for _, id := range []string{
		"",
		"../../etc",
		"UPPERCASE",
		"has-dash",
		"has.dot",
		"has/slash",
		strings.Repeat("a", 41),
	} {
		if _, err := store.GetKey(ctx, alice.ID(), id); !errors.Is(err, ks.ErrNotFound) {
			t.Errorf("GetKey(%q) = %v, want ErrNotFound", id, err)
		}
		if err := store.RevokeKey(ctx, alice.ID(), id); !errors.Is(err, ks.ErrNotFound) {
			t.Errorf("RevokeKey(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestRevokeRemovesOnlyThatEntry(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t, bootstrapSecret())

	first, err := store.CreateKey(ctx, alice, "first")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	second, err := store.CreateKey(ctx, alice, "second")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := store.RevokeKey(ctx, alice.ID(), first.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	secret := readSecret(t, client)
	if _, ok := secret.Data["client-"+first.ID]; ok {
		t.Error("revoked entry is still present")
	}
	if _, ok := secret.Annotations["llm-portal/key-"+first.ID]; ok {
		t.Error("revoked key's ownership annotation is still present")
	}
	if _, ok := secret.Data["client-"+second.ID]; !ok {
		t.Error("revoke removed an unrelated key")
	}
	// The bootstrap entry and the aggregate Secret itself must survive.
	if string(secret.Data["client-test"]) != "llm_testkey123" {
		t.Error("revoke disturbed the bootstrap entry")
	}

	if _, err := store.GetKey(ctx, alice.ID(), first.ID); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("GetKey after revoke = %v, want ErrNotFound", err)
	}
}

func TestReadyChecksTheAPI(t *testing.T) {
	store, _ := newTestStore(t, bootstrapSecret())
	if err := store.Ready(context.Background()); err != nil {
		t.Errorf("Ready: %v", err)
	}
}

// A not-yet-created aggregate Secret is still ready: the first CreateKey
// bootstraps it.
func TestReadyWhenSecretMissing(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Ready(context.Background()); err != nil {
		t.Errorf("Ready with no secret yet = %v, want nil", err)
	}
}

func TestNewRequiresConfiguration(t *testing.T) {
	client := fake.NewClientset()
	tests := []struct {
		name string
		opts Options
	}{
		{"no client", Options{Namespace: "n", SecretName: "s"}},
		{"no namespace", Options{Client: client, SecretName: "s"}},
		{"no secret name", Options{Client: client, Namespace: "n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Error("New succeeded, want an error")
			}
		})
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
