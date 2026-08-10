// Package keystore defines how API-key credentials are persisted and who may
// operate on them.
//
// Handlers depend on the KeyStore interface only. The production
// implementation talks to the Kubernetes API; the memory implementation backs
// local development and tests.
package keystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors returned by implementations. Handlers map these to user-facing copy.
var (
	// ErrNotFound is returned both when a key does not exist and when it
	// belongs to someone else. Callers must not distinguish the two, so that a
	// probe cannot confirm the existence of another user's key.
	ErrNotFound = errors.New("key not found")

	// ErrInvalidName reports a rejected display name.
	ErrInvalidName = errors.New("invalid key name")
)

// Owner is the canonical identity of a user, as established by Entra.
//
// Tenant and object IDs are stable and non-reassignable; email and display name
// are not, and are carried only so the stored object is legible to a human
// debugging the cluster.
type Owner struct {
	TenantID string
	ObjectID string

	DisplayName string
	Email       string
}

// ID is the authorization key: the tuple that must match for any operation.
func (o Owner) ID() OwnerID { return OwnerID{TenantID: o.TenantID, ObjectID: o.ObjectID} }

// OwnerID is the identity tuple without display fields.
type OwnerID struct {
	TenantID string
	ObjectID string
}

// Valid reports whether both halves of the identity are present. An empty
// tenant or object ID must never be used for a lookup, since it would match
// broadly.
func (o OwnerID) Valid() bool { return o.TenantID != "" && o.ObjectID != "" }

// KeyMetadata is everything we can show about an existing key. It deliberately
// has no field for the credential itself: once created, the cleartext is
// unavailable to the portal by design.
type KeyMetadata struct {
	ID        string // opaque resource identifier, safe in URLs
	Name      string // user-supplied display name
	Suffix    string // last few characters of the credential, for recognition
	CreatedAt time.Time
}

// CreatedKey is returned exactly once, from CreateKey.
type CreatedKey struct {
	KeyMetadata

	// Secret is the cleartext credential. It is rendered on the one-time page
	// and then dropped. It must never be logged, redirected, or persisted
	// anywhere outside the Kubernetes Secret.
	Secret string
}

// KeyStore persists API-key credentials.
//
// Every method takes the caller's identity and is responsible for enforcing
// ownership itself, so a handler cannot forget to check.
type KeyStore interface {
	// ListKeys returns the caller's keys, newest first.
	ListKeys(ctx context.Context, owner OwnerID) ([]KeyMetadata, error)

	// GetKey returns a single key owned by the caller, for the revoke
	// confirmation page. It returns ErrNotFound when the key does not exist or
	// is owned by someone else.
	GetKey(ctx context.Context, owner OwnerID, keyID string) (KeyMetadata, error)

	// CreateKey generates a new credential owned by the caller.
	CreateKey(ctx context.Context, owner Owner, displayName string) (CreatedKey, error)

	// RevokeKey deletes a key. It returns ErrNotFound when the key does not
	// exist or is owned by someone else.
	RevokeKey(ctx context.Context, owner OwnerID, keyID string) error

	// Ready reports whether the store is usable, for readiness probes. It must
	// not mutate anything.
	Ready(ctx context.Context) error
}

// maxNameLen bounds the user-supplied display name.
const maxNameLen = 64

// ValidateName cleans and checks a user-supplied key name.
//
// The name is the only field a user controls, and it ends up in a Kubernetes
// annotation as well as in HTML, so it is checked strictly here rather than at
// each use site.
func ValidateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: give the key a name so you can recognise it later", ErrInvalidName)
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: name contains invalid characters", ErrInvalidName)
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", fmt.Errorf("%w: name must be %d characters or fewer", ErrInvalidName, maxNameLen)
	}
	for _, r := range name {
		// Control characters would corrupt annotation values and log lines.
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: name must not contain control characters", ErrInvalidName)
		}
	}
	return name, nil
}
