package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

var (
	alice = keystore.Owner{TenantID: "tenant-a", ObjectID: "oid-alice", DisplayName: "Alice"}
	bob   = keystore.Owner{TenantID: "tenant-a", ObjectID: "oid-bob", DisplayName: "Bob"}

	// sameOIDOtherTenant shares Bob's object ID in a different tenant. Object
	// IDs are only unique within a tenant, so the pair must be compared, never
	// the object ID alone.
	sameOIDOtherTenant = keystore.Owner{TenantID: "tenant-b", ObjectID: "oid-bob"}
)

func TestCreateAndList(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	created, err := store.CreateKey(ctx, alice, "  MacBook  ")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if created.Name != "MacBook" {
		t.Errorf("Name = %q, want the trimmed name", created.Name)
	}
	if created.Secret == "" {
		t.Fatal("CreateKey returned an empty credential")
	}

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("ListKeys returned %d keys, want 1", len(keys))
	}
	if keys[0].Suffix != created.Suffix {
		t.Errorf("listed suffix = %q, want %q", keys[0].Suffix, created.Suffix)
	}
}

// The credential must be unrecoverable after creation. Nothing on the read
// paths may expose it.
func TestCredentialIsNotRecoverable(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	created, err := store.CreateKey(ctx, alice, "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	for _, k := range keys {
		if k.Suffix == created.Secret || k.Name == created.Secret || k.ID == created.Secret {
			t.Fatal("listed metadata contains the credential")
		}
	}

	got, err := store.GetKey(ctx, alice.ID(), created.ID)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.Suffix == created.Secret {
		t.Fatal("GetKey exposed the credential")
	}
}

func TestListIsScopedToOwner(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	if _, err := store.CreateKey(ctx, alice, "Alice key"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if _, err := store.CreateKey(ctx, bob, "Bob key"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	aliceKeys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(aliceKeys) != 1 || aliceKeys[0].Name != "Alice key" {
		t.Fatalf("Alice sees %+v, want only her own key", aliceKeys)
	}
}

func TestAnotherUserCannotReadOrRevoke(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	created, err := store.CreateKey(ctx, alice, "Alice key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Bob knows the ID and tries to use it directly.
	if _, err := store.GetKey(ctx, bob.ID(), created.ID); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("GetKey by another user = %v, want ErrNotFound", err)
	}
	if err := store.RevokeKey(ctx, bob.ID(), created.ID); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("RevokeKey by another user = %v, want ErrNotFound", err)
	}

	// The key must still be there.
	if _, err := store.GetKey(ctx, alice.ID(), created.ID); err != nil {
		t.Errorf("key was affected by another user's revoke attempt: %v", err)
	}
}

func TestSameObjectIDInAnotherTenantIsADifferentPerson(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	created, err := store.CreateKey(ctx, bob, "Bob key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := store.RevokeKey(ctx, sameOIDOtherTenant.ID(), created.ID); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("RevokeKey across tenants = %v, want ErrNotFound", err)
	}
	keys, err := store.ListKeys(ctx, sameOIDOtherTenant.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("cross-tenant list returned %d keys, want 0", len(keys))
	}
}

func TestRevokeRemovesTheKey(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	created, err := store.CreateKey(ctx, alice, "MacBook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := store.RevokeKey(ctx, alice.ID(), created.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	keys, err := store.ListKeys(ctx, alice.ID())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("ListKeys returned %d keys after revoke, want 0", len(keys))
	}
	// Revoking twice is not an error condition worth distinguishing.
	if err := store.RevokeKey(ctx, alice.ID(), created.ID); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("second RevokeKey = %v, want ErrNotFound", err)
	}
}

func TestEmptyIdentityIsRejected(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	if _, err := store.CreateKey(ctx, alice, "MacBook"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	// A zero identity must not act as a wildcard that matches every key.
	keys, err := store.ListKeys(ctx, keystore.OwnerID{})
	if err == nil && len(keys) != 0 {
		t.Fatalf("empty identity listed %d keys, want none", len(keys))
	}
}

func TestInvalidNameIsRejected(t *testing.T) {
	ctx := context.Background()
	store := New("llm_")

	if _, err := store.CreateKey(ctx, alice, "   "); !errors.Is(err, keystore.ErrInvalidName) {
		t.Errorf("CreateKey with a blank name = %v, want ErrInvalidName", err)
	}
}
