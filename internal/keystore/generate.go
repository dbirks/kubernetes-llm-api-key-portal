package keystore

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	// secretBytes is the amount of random material behind each credential.
	// 256 bits is the floor set by the design brief.
	secretBytes = 32

	// suffixLen is how much of the credential we retain in metadata so a user
	// can tell their keys apart. Short enough to be useless to an attacker,
	// long enough to disambiguate a handful of keys.
	suffixLen = 6

	// idBytes backs the opaque resource identifier. This is not a secret, but
	// it should be unguessable enough that key IDs are not enumerable.
	idBytes = 8
)

// Generated is the raw material for one new credential, before it is handed to
// a storage implementation.
type Generated struct {
	// ID is the opaque resource identifier. It contains no user identity, so
	// it is safe in URLs and gateway logs.
	ID string

	// ClientID is the per-credential identifier that the gateway reports when
	// attributing a request. Also opaque and non-secret.
	ClientID string

	// Secret is the cleartext credential.
	Secret string

	// Suffix is the trailing characters of Secret, retained in metadata.
	Suffix string
}

// Generate creates a new credential using crypto/rand.
//
// The credential format is <prefix><base64url>, which is greppable in a config
// file and recognisable when a user pastes the wrong string somewhere.
func Generate(prefix string) (Generated, error) {
	secretRaw := make([]byte, secretBytes)
	if _, err := rand.Read(secretRaw); err != nil {
		return Generated{}, fmt.Errorf("generate credential: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(secretRaw)

	id, err := randomID(idBytes)
	if err != nil {
		return Generated{}, err
	}
	clientID, err := randomID(idBytes)
	if err != nil {
		return Generated{}, err
	}

	return Generated{
		ID:       id,
		ClientID: "client-" + clientID,
		Secret:   prefix + encoded,
		Suffix:   encoded[len(encoded)-suffixLen:],
	}, nil
}

// randomID returns a lowercase base32-style identifier safe for use in
// Kubernetes resource names, which must be lowercase alphanumeric with dashes.
func randomID(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	// Base32 without padding, lowercased, is RFC 1123 label-safe.
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}
