// Package kubernetes persists API-key credentials in a single Kubernetes Secret.
//
// Everything goes through the Kubernetes API server; the portal never touches
// etcd. Envoy Gateway's SecurityPolicy.apiKeyAuth reads every valid credential
// from ONE aggregate Opaque Secret referenced by name — there is no label
// selector, and no per-key Secret. So the portal keeps all issued keys as
// entries in that one Secret: each data entry is `client-<id>: <credential>`,
// and the credential is compared verbatim by the gateway.
//
// Ownership cannot live on a per-key label any more (there is one object), so it
// is recorded in a per-key annotation on the same Secret. That lets the account
// page list and revoke a user's own keys while a key's data entry stays a bare
// client-id -> credential mapping the gateway understands.
package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	ks "github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

// clientKeyPrefix prefixes every data-entry key in the aggregate Secret. Its
// value is the credential the gateway accepts; the key (e.g. "client-abcd") is
// the opaque client identifier the gateway attributes a request to.
const clientKeyPrefix = "client-"

// LabelDomain prefixes every annotation this package writes.
//
// It is a constant, not configuration. Exposing it as an environment variable
// made it look like a per-deployment preference, which invited a silent
// mismatch. The annotations it prefixes are internal to the portal — the
// gateway never reads them — so the value only has to be stable across the
// portal's own upgrades.
//
// Deliberately not a hostname. An annotation prefix outlives whatever DNS name
// the portal happens to be served from. Kubernetes accepts any DNS subdomain,
// including a single segment like this one.
const LabelDomain = "llm-portal"

// keyAnnPrefix, joined to LabelDomain, prefixes the per-key ownership+metadata
// annotation. The full key is "<LabelDomain>/key-<id>".
const keyAnnPrefix = "key-"

// managedBy marks the aggregate Secret the first time the portal creates it, so
// a human reading the cluster can tell what writes to it. It is never relied on
// for correctness — the SOPS-bootstrapped Secret will not carry it.
const (
	annManagedBy   = "managed-by"
	managedByValue = "ai-account"
)

// validKeyID guards anything taken from a URL before it becomes part of a data
// key or annotation name.
var validKeyID = regexp.MustCompile(`^[a-z0-9]{1,40}$`)

// keyMeta is the per-key record stored (as JSON) in the "<domain>/key-<id>"
// annotation. It carries the owning identity — the authorization tuple checked
// on every list/get/revoke — plus the fields needed to render the account page.
// The credential itself is never here; it lives only in the Secret's data.
type keyMeta struct {
	TenantID   string    `json:"tid"`
	ObjectID   string    `json:"oid"`
	Name       string    `json:"name"`
	Suffix     string    `json:"suffix"`
	OwnerName  string    `json:"owner_name,omitempty"`
	OwnerEmail string    `json:"owner_email,omitempty"`
	CreatedAt  time.Time `json:"created"`
}

// Options configures the store.
type Options struct {
	// Client is the Kubernetes clientset. Injectable so tests can use the fake.
	Client kubernetes.Interface

	// Namespace holds the aggregate API-key Secret. This is a security
	// boundary: Kubernetes RBAC restricts Secret access by namespace and name,
	// not by label.
	Namespace string

	// SecretName is the single aggregate Secret the gateway reads keys from.
	SecretName string

	// KeyPrefix is prepended to generated credentials, e.g. "llm_".
	KeyPrefix string
}

// Store is the production KeyStore.
type Store struct {
	client     kubernetes.Interface
	namespace  string
	secretName string
	keyPrefix  string
}

// New validates options and returns a store.
func New(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	if opts.Namespace == "" {
		return nil, errors.New("namespace is required")
	}
	if opts.SecretName == "" {
		return nil, errors.New("secret name is required")
	}
	return &Store{
		client:     opts.Client,
		namespace:  opts.Namespace,
		secretName: opts.SecretName,
		keyPrefix:  opts.KeyPrefix,
	}, nil
}

func (s *Store) annKey(id string) string { return LabelDomain + "/" + keyAnnPrefix + id }

func (s *Store) secrets() typedcorev1.SecretInterface {
	return s.client.CoreV1().Secrets(s.namespace)
}

// get fetches the aggregate Secret. A missing Secret is reported as such so
// callers can distinguish "no keys yet" from a real error.
func (s *Store) get(ctx context.Context) (*corev1.Secret, error) {
	return s.secrets().Get(ctx, s.secretName, metav1.GetOptions{})
}

func (s *Store) ListKeys(ctx context.Context, owner ks.OwnerID) ([]ks.KeyMetadata, error) {
	if !owner.Valid() {
		return nil, ks.ErrNotFound
	}
	secret, err := s.get(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get api key secret: %w", err)
	}

	var out []ks.KeyMetadata
	for dataKey := range secret.Data {
		id, ok := strings.CutPrefix(dataKey, clientKeyPrefix)
		if !ok {
			continue
		}
		meta, ok := s.ownedMeta(secret, owner, id)
		if !ok {
			continue
		}
		out = append(out, meta)
	}
	// Newest first, with the ID as a stable tiebreaker.
	sortByCreatedDesc(out)
	return out, nil
}

func (s *Store) GetKey(ctx context.Context, owner ks.OwnerID, keyID string) (ks.KeyMetadata, error) {
	if !owner.Valid() || !validKeyID.MatchString(keyID) {
		return ks.KeyMetadata{}, ks.ErrNotFound
	}
	secret, err := s.get(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ks.KeyMetadata{}, ks.ErrNotFound
		}
		return ks.KeyMetadata{}, fmt.Errorf("get api key secret: %w", err)
	}
	meta, ok := s.ownedMeta(secret, owner, keyID)
	if !ok {
		return ks.KeyMetadata{}, ks.ErrNotFound
	}
	return meta, nil
}

// ownedMeta returns the metadata for one key if it both exists as a data entry
// and is owned by the caller. Ownership is checked against the annotation on the
// stored object, never against anything supplied by the browser. A key owned by
// someone else — or a bare data entry with no ownership annotation, such as the
// bootstrap test key — is reported as absent, identically to a missing one, so
// this cannot be used to enumerate other people's keys.
func (s *Store) ownedMeta(secret *corev1.Secret, owner ks.OwnerID, id string) (ks.KeyMetadata, bool) {
	if _, present := secret.Data[clientKeyPrefix+id]; !present {
		return ks.KeyMetadata{}, false
	}
	raw, ok := secret.Annotations[s.annKey(id)]
	if !ok {
		return ks.KeyMetadata{}, false
	}
	var m keyMeta
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ks.KeyMetadata{}, false
	}
	if m.TenantID != owner.TenantID || m.ObjectID != owner.ObjectID {
		return ks.KeyMetadata{}, false
	}
	return ks.KeyMetadata{
		ID:        id,
		Name:      m.Name,
		Suffix:    m.Suffix,
		CreatedAt: m.CreatedAt,
	}, true
}

func (s *Store) CreateKey(ctx context.Context, owner ks.Owner, displayName string) (ks.CreatedKey, error) {
	if !owner.ID().Valid() {
		return ks.CreatedKey{}, ks.ErrNotFound
	}
	name, err := ks.ValidateName(displayName)
	if err != nil {
		return ks.CreatedKey{}, err
	}
	gen, err := ks.Generate(s.keyPrefix)
	if err != nil {
		return ks.CreatedKey{}, err
	}

	createdAt := time.Now().UTC()
	meta := keyMeta{
		TenantID:   owner.TenantID,
		ObjectID:   owner.ObjectID,
		Name:       name,
		Suffix:     gen.Suffix,
		OwnerName:  owner.DisplayName,
		OwnerEmail: owner.Email,
		CreatedAt:  createdAt,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return ks.CreatedKey{}, fmt.Errorf("encode key metadata: %w", err)
	}

	dataKey := clientKeyPrefix + gen.ID

	// Add this key as one entry in the aggregate Secret. A JSON merge patch
	// touches only the two map entries we name and leaves every other key
	// untouched, so concurrent issues by different users don't race.
	patch := map[string]any{
		"data": map[string]any{
			// Secret data is base64 over the wire; the value is the bare
			// credential the gateway compares against, no "Bearer " prefix.
			dataKey: base64.StdEncoding.EncodeToString([]byte(gen.Secret)),
		},
		"metadata": map[string]any{
			"annotations": map[string]any{
				s.annKey(gen.ID): string(metaJSON),
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return ks.CreatedKey{}, fmt.Errorf("encode patch: %w", err)
	}

	_, err = s.secrets().Patch(ctx, s.secretName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		// First key ever, and the aggregate Secret was not bootstrapped: create
		// it. A concurrent creator wins the race harmlessly — retry as a patch.
		if cerr := s.createSecret(ctx, dataKey, gen.Secret, s.annKey(gen.ID), string(metaJSON)); cerr != nil {
			if apierrors.IsAlreadyExists(cerr) {
				_, err = s.secrets().Patch(ctx, s.secretName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
			} else {
				err = cerr
			}
		} else {
			err = nil
		}
	}
	if err != nil {
		return ks.CreatedKey{}, fmt.Errorf("upsert api key entry: %w", err)
	}

	return ks.CreatedKey{
		KeyMetadata: ks.KeyMetadata{
			ID:        gen.ID,
			Name:      name,
			Suffix:    gen.Suffix,
			CreatedAt: createdAt,
		},
		Secret: gen.Secret,
	}, nil
}

// createSecret creates the aggregate Secret with a single initial entry, for the
// case where it was never bootstrapped in the cluster repository.
func (s *Store) createSecret(ctx context.Context, dataKey, value, annName, annValue string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.secretName,
			Namespace: s.namespace,
			Annotations: map[string]string{
				LabelDomain + "/" + annManagedBy: managedByValue,
				annName:                          annValue,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dataKey: []byte(value)},
	}
	_, err := s.secrets().Create(ctx, secret, metav1.CreateOptions{})
	return err
}

func (s *Store) RevokeKey(ctx context.Context, owner ks.OwnerID, keyID string) error {
	if !owner.Valid() || !validKeyID.MatchString(keyID) {
		return ks.ErrNotFound
	}
	secret, err := s.get(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ks.ErrNotFound
		}
		return fmt.Errorf("get api key secret: %w", err)
	}
	// Confirm the caller owns this key before removing anything.
	if _, ok := s.ownedMeta(secret, owner, keyID); !ok {
		return ks.ErrNotFound
	}

	// Remove both the credential entry and its ownership annotation. In a JSON
	// merge patch, a null value deletes that map key.
	patch := map[string]any{
		"data": map[string]any{
			clientKeyPrefix + keyID: nil,
		},
		"metadata": map[string]any{
			"annotations": map[string]any{
				s.annKey(keyID): nil,
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode patch: %w", err)
	}
	if _, err := s.secrets().Patch(ctx, s.secretName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return ks.ErrNotFound
		}
		return fmt.Errorf("remove api key entry: %w", err)
	}
	return nil
}

// Ready confirms the API server is reachable and the ServiceAccount may read the
// aggregate Secret in the namespace. A not-yet-created Secret still counts as
// ready: the first CreateKey will bootstrap it.
func (s *Store) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := s.get(ctx); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("keystore not ready: %w", err)
	}
	return nil
}

func sortByCreatedDesc(keys []ks.KeyMetadata) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0; j-- {
			a, b := keys[j-1], keys[j]
			newer := b.CreatedAt.After(a.CreatedAt) ||
				(b.CreatedAt.Equal(a.CreatedAt) && b.ID < a.ID)
			if !newer {
				break
			}
			keys[j-1], keys[j] = b, a
		}
	}
}

var _ ks.KeyStore = (*Store)(nil)
