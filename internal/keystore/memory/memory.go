// Package memory is an in-process KeyStore for local development and tests.
//
// It enforces the same ownership rules as the Kubernetes implementation, so a
// handler test that passes here is exercising the real authorization logic.
// Nothing is persisted; restarting the process discards every key.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

// Store is a concurrency-safe in-memory KeyStore.
type Store struct {
	prefix string

	mu      sync.RWMutex
	records map[string]record

	// now is swappable so tests can produce deterministic timestamps.
	now func() time.Time
}

type record struct {
	owner keystore.OwnerID
	meta  keystore.KeyMetadata
}

// New returns an empty store that mints credentials with the given prefix.
func New(prefix string) *Store {
	return &Store{
		prefix:  prefix,
		records: make(map[string]record),
		now:     time.Now,
	}
}

// SetClock overrides the time source. For tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Seed inserts a key with a fixed name and creation time, without generating a
// usable credential. It exists so local development and UI work can start from
// a populated account page.
func (s *Store) Seed(owner keystore.Owner, name, suffix string, createdAt time.Time) keystore.KeyMetadata {
	gen, err := keystore.Generate(s.prefix)
	if err != nil {
		panic("memory keystore: " + err.Error())
	}
	meta := keystore.KeyMetadata{
		ID:        gen.ID,
		Name:      name,
		Suffix:    suffix,
		CreatedAt: createdAt,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[meta.ID] = record{owner: owner.ID(), meta: meta}
	return meta
}

func (s *Store) ListKeys(ctx context.Context, owner keystore.OwnerID) ([]keystore.KeyMetadata, error) {
	if !owner.Valid() {
		return nil, keystore.ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []keystore.KeyMetadata
	for _, rec := range s.records {
		if rec.owner == owner {
			out = append(out, rec.meta)
		}
	}
	// Newest first, with the ID as a tiebreaker so ordering is stable when
	// several keys share a timestamp.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) CreateKey(ctx context.Context, owner keystore.Owner, displayName string) (keystore.CreatedKey, error) {
	if !owner.ID().Valid() {
		return keystore.CreatedKey{}, keystore.ErrNotFound
	}
	name, err := keystore.ValidateName(displayName)
	if err != nil {
		return keystore.CreatedKey{}, err
	}
	gen, err := keystore.Generate(s.prefix)
	if err != nil {
		return keystore.CreatedKey{}, err
	}

	meta := keystore.KeyMetadata{
		ID:        gen.ID,
		Name:      name,
		Suffix:    gen.Suffix,
		CreatedAt: s.now().UTC(),
	}
	s.mu.Lock()
	s.records[meta.ID] = record{owner: owner.ID(), meta: meta}
	s.mu.Unlock()

	return keystore.CreatedKey{KeyMetadata: meta, Secret: gen.Secret}, nil
}

func (s *Store) RevokeKey(ctx context.Context, owner keystore.OwnerID, keyID string) error {
	if !owner.Valid() || keyID == "" {
		return keystore.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[keyID]
	// A key owned by someone else is reported as missing, so this endpoint
	// cannot be used to discover which key IDs exist.
	if !ok || rec.owner != owner {
		return keystore.ErrNotFound
	}
	delete(s.records, keyID)
	return nil
}

func (s *Store) GetKey(ctx context.Context, owner keystore.OwnerID, keyID string) (keystore.KeyMetadata, error) {
	if !owner.Valid() || keyID == "" {
		return keystore.KeyMetadata{}, keystore.ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[keyID]
	if !ok || rec.owner != owner {
		return keystore.KeyMetadata{}, keystore.ErrNotFound
	}
	return rec.meta, nil
}

// Ready always succeeds: an in-memory store has no dependency to check.
func (s *Store) Ready(ctx context.Context) error { return nil }
