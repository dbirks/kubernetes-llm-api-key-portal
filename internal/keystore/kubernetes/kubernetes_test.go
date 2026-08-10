package kubernetes

import (
	"context"
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
	testNamespace = "llm-access"
	testDomain    = "ai.birks.dev"
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
		Client:      client,
		Namespace:   testNamespace,
		LabelDomain: testDomain,
		KeyPrefix:   "llm_",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, client
}

// TestCreatedSecretShape pins the exact object written to the cluster. The
// gateway selects on these labels and expects this type, so a change here is a
// change to the integration contract with the cluster repository.
func TestCreatedSecretShape(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t)

	created, err := store.CreateKey(ctx, alice, "MacBook Claude Code")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, "llm-key-"+created.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading back the created Secret: %v", err)
	}

	if got, want := string(secret.Type), SecretType; got != want {
		t.Errorf("Secret type = %q, want %q", got, want)
	}
	if secret.Immutable == nil || !*secret.Immutable {
		t.Error("Secret is not immutable; keys must never be edited in place")
	}

	wantLabels := map[string]string{
		testDomain + "/api-key":   "true",
		testDomain + "/owner-tid": alice.TenantID,
		testDomain + "/owner-oid": alice.ObjectID,
	}
	for k, want := range wantLabels {
		if got := secret.Labels[k]; got != want {
			t.Errorf("label %s = %q, want %q", k, got, want)
		}
	}

	wantAnnotations := map[string]string{
		testDomain + "/display-name":       "MacBook Claude Code",
		testDomain + "/key-suffix":         created.Suffix,
		testDomain + "/owner-display-name": alice.DisplayName,
		testDomain + "/owner-email":        alice.Email,
		testDomain + "/managed-by":         managedByValue,
	}
	for k, want := range wantAnnotations {
		if got := secret.Annotations[k]; got != want {
			t.Errorf("annotation %s = %q, want %q", k, got, want)
		}
	}

	// Exactly one data entry: the gateway client identifier mapped to the
	// credential.
	if len(secret.StringData) != 1 {
		t.Fatalf("Secret has %d data entries, want 1", len(secret.StringData))
	}
	for clientID, value := range secret.StringData {
		if !strings.HasPrefix(clientID, "client-") {
			t.Errorf("data key = %q, want a client- prefix", clientID)
		}
		if value != created.Secret {
			t.Error("stored value does not match the returned credential")
		}
	}
}

// The Secret name and client identifier are visible in the cluster and may
// appear in gateway logs, so neither may carry the user's identity.
func TestSecretNameCarriesNoIdentity(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t)

	created, err := store.CreateKey(ctx, alice, "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, "llm-key-"+created.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	for _, forbidden := range []string{alice.Email, alice.ObjectID, alice.DisplayName, "alice"} {
		if strings.Contains(strings.ToLower(secret.Name), strings.ToLower(forbidden)) {
			t.Errorf("Secret name %q leaks %q", secret.Name, forbidden)
		}
		for clientID := range secret.StringData {
			if strings.Contains(strings.ToLower(clientID), strings.ToLower(forbidden)) {
				t.Errorf("client identifier %q leaks %q", clientID, forbidden)
			}
		}
	}
}

func TestListIsScopedToOwnerLabels(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

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

func TestAnotherUserCannotRevoke(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t)

	created, err := store.CreateKey(ctx, alice, "Alice key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := store.RevokeKey(ctx, bob.ID(), created.ID); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("RevokeKey by another user = %v, want ErrNotFound", err)
	}
	if _, err := client.CoreV1().Secrets(testNamespace).Get(ctx, "llm-key-"+created.ID, metav1.GetOptions{}); err != nil {
		t.Errorf("Secret was deleted by a user who does not own it: %v", err)
	}
}

// A key ID arrives from the URL, so it must be rejected before it can be
// interpolated into a Kubernetes resource name.
func TestMalformedKeyIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

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

func TestRevokeDeletesTheSecret(t *testing.T) {
	ctx := context.Background()
	store, client := newTestStore(t)

	created, err := store.CreateKey(ctx, alice, "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := store.RevokeKey(ctx, alice.ID(), created.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	list, err := client.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("%d Secrets remain after revoke, want 0", len(list.Items))
	}
}

// A Secret in the namespace that the portal did not create, or one belonging to
// nobody, must not be readable through the portal.
func TestUnlabelledSecretIsInvisible(t *testing.T) {
	ctx := context.Background()
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "llm-key-deadbeef", Namespace: testNamespace},
		Data:       map[string][]byte{"client-x": []byte("someone-elses-secret")},
	}
	store, _ := newTestStore(t, foreign)

	if _, err := store.GetKey(ctx, alice.ID(), "deadbeef"); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("GetKey on an unlabelled Secret = %v, want ErrNotFound", err)
	}
	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("ListKeys returned %d keys, want 0", len(keys))
	}
}

func TestReadyChecksTheAPI(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Ready(context.Background()); err != nil {
		t.Errorf("Ready: %v", err)
	}
}

func TestNewRequiresConfiguration(t *testing.T) {
	client := fake.NewClientset()
	tests := []struct {
		name string
		opts Options
	}{
		{"no client", Options{Namespace: "n", LabelDomain: "d"}},
		{"no namespace", Options{Client: client, LabelDomain: "d"}},
		{"no label domain", Options{Client: client, Namespace: "n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Error("New succeeded, want an error")
			}
		})
	}
}
