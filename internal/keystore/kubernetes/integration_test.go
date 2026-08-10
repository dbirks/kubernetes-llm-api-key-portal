package kubernetes

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ks "github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

// These tests run against a real Kubernetes API server. They are skipped unless
// PORTAL_INTEGRATION_NAMESPACE names a namespace to use.
//
//	kubectl create namespace portal-itest
//	PORTAL_INTEGRATION_NAMESPACE=portal-itest go test ./internal/keystore/kubernetes -run Integration -v
//
// The fake clientset used by the other tests in this package validates the
// object we construct and our own authorization logic, but it cannot tell us
// whether the API server accepts that object, whether the ServiceAccount has
// the right RBAC, or whether immutability is genuinely enforced. Those are the
// gaps this file covers.
//
// Every object it creates is deleted on the way out. Point it at a scratch
// namespace regardless.

func integrationStore(t *testing.T) (*Store, string) {
	t.Helper()

	namespace := strings.TrimSpace(os.Getenv("PORTAL_INTEGRATION_NAMESPACE"))
	if namespace == "" {
		t.Skip("set PORTAL_INTEGRATION_NAMESPACE to run against a real cluster")
	}

	client, err := NewClient(ClientOptions{
		Kubeconfig:      os.Getenv("KUBECONFIG"),
		AllowKubeconfig: true,
	})
	if err != nil {
		t.Fatalf("connecting to the cluster: %v", err)
	}
	store, err := New(Options{Client: client, Namespace: namespace, KeyPrefix: "llm_"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, namespace
}

func TestIntegrationKeyLifecycle(t *testing.T) {
	store, namespace := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Readiness doubles as an RBAC check: it fails if the ServiceAccount or
	// user cannot list Secrets in this namespace.
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	created, err := store.CreateKey(ctx, alice, "integration test key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	t.Cleanup(func() {
		// Best effort: the revoke assertions below normally remove it already.
		_ = store.RevokeKey(context.Background(), alice.ID(), created.ID)
	})

	t.Logf("created %s/%s%s", namespace, secretNamePrefix, created.ID)

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	var found bool
	for _, k := range keys {
		if k.ID == created.ID {
			found = true
			if k.Name != "integration test key" {
				t.Errorf("listed name = %q", k.Name)
			}
			if k.Suffix != created.Suffix {
				t.Errorf("listed suffix = %q, want %q", k.Suffix, created.Suffix)
			}
			// The API server stamps this, so a zero value means we read the
			// wrong field.
			if k.CreatedAt.IsZero() {
				t.Error("CreatedAt was not populated from the API server")
			}
		}
	}
	if !found {
		t.Fatal("the created key did not come back from a label-selector list")
	}

	// Ownership is enforced against the stored labels, in a real cluster.
	if err := store.RevokeKey(ctx, bob.ID(), created.ID); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("RevokeKey by another user = %v, want ErrNotFound", err)
	}

	if err := store.RevokeKey(ctx, alice.ID(), created.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if _, err := store.GetKey(ctx, alice.ID(), created.ID); !errors.Is(err, ks.ErrNotFound) {
		t.Errorf("GetKey after revoke = %v, want ErrNotFound", err)
	}
}

// Immutability is enforced by the API server, not by us. This confirms the flag
// actually takes effect, which the fake clientset does not simulate.
func TestIntegrationSecretIsImmutable(t *testing.T) {
	store, namespace := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := store.CreateKey(ctx, alice, "immutability check")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	t.Cleanup(func() {
		_ = store.RevokeKey(context.Background(), alice.ID(), created.ID)
	})

	secrets := store.client.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, secretNamePrefix+created.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	secret.Data["client-injected"] = []byte("attacker-supplied")
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err == nil {
		t.Error("the API server allowed an immutable Secret to be updated")
	}
}
