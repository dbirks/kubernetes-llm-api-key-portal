// Package kubernetes persists API-key credentials as Kubernetes Secrets.
//
// Everything goes through the Kubernetes API server; the portal never touches
// etcd. Each credential is one immutable Secret, labelled so that the gateway
// can select it and so that the portal can find the ones a given user owns.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	ks "github.com/dbirks/kubernetes-llm-api-key-portal/internal/keystore"
)

// SecretType is the type kgateway expects for API-key Secrets.
const SecretType = "extauth.solo.io/apikey"

// secretNamePrefix keeps generated Secrets identifiable in a shared namespace.
const secretNamePrefix = "llm-key-"

// LabelDomain prefixes every label and annotation this package writes.
//
// It is a constant, not configuration. This value is one half of a contract
// with the gateway's TrafficPolicy secretSelector in the cluster repository,
// and the two must be changed together in the same breath. Exposing it as an
// environment variable made it look like a per-deployment preference, which
// invited exactly the mismatch that silently stops every key from
// authenticating.
//
// Deliberately not a hostname. A label prefix outlives whatever DNS name the
// portal happens to be served from, and burying the current one here would
// mean either a misleading label or a migration of every Secret the next time
// the site moves. Kubernetes accepts any DNS subdomain, including a single
// segment like this one.
const LabelDomain = "llm-portal"

// Label and annotation key suffixes, joined to LabelDomain.
const (
	labelAPIKey   = "api-key"
	labelOwnerOID = "owner-oid"
	labelOwnerTID = "owner-tid"

	annDisplayName      = "display-name"
	annKeySuffix        = "key-suffix"
	annOwnerDisplayName = "owner-display-name"
	annOwnerEmail       = "owner-email"
	annManagedBy        = "managed-by"

	managedByValue = "ai-account"
)

// validKeyID guards anything taken from a URL before it becomes part of a
// Kubernetes resource name.
var validKeyID = regexp.MustCompile(`^[a-z0-9]{1,40}$`)

// Options configures the store.
type Options struct {
	// Client is the Kubernetes clientset. Injectable so tests can use the fake.
	Client kubernetes.Interface

	// Namespace is the dedicated namespace holding API-key Secrets. This is a
	// security boundary: Kubernetes RBAC cannot restrict Secret access by
	// label, only by namespace and name.
	Namespace string

	// KeyPrefix is prepended to generated credentials, e.g. "llm_".
	KeyPrefix string
}

// Store is the production KeyStore.
type Store struct {
	client    kubernetes.Interface
	namespace string
	keyPrefix string
}

// New validates options and returns a store.
func New(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	if opts.Namespace == "" {
		return nil, errors.New("namespace is required")
	}
	return &Store{
		client:    opts.Client,
		namespace: opts.Namespace,
		keyPrefix: opts.KeyPrefix,
	}, nil
}

func (s *Store) label(name string) string { return LabelDomain + "/" + name }

func (s *Store) secrets() typedcorev1.SecretInterface {
	return s.client.CoreV1().Secrets(s.namespace)
}

// ownerSelector builds the label selector for one user's keys.
//
// Filtering happens on the API server rather than in the portal, so a user's
// list request never retrieves another user's Secret data.
func (s *Store) ownerSelector(owner ks.OwnerID) string {
	return labels.Set{
		s.label(labelAPIKey):   "true",
		s.label(labelOwnerTID): owner.TenantID,
		s.label(labelOwnerOID): owner.ObjectID,
	}.String()
}

func (s *Store) ListKeys(ctx context.Context, owner ks.OwnerID) ([]ks.KeyMetadata, error) {
	if !owner.Valid() {
		return nil, ks.ErrNotFound
	}
	list, err := s.secrets().List(ctx, metav1.ListOptions{
		LabelSelector: s.ownerSelector(owner),
	})
	if err != nil {
		return nil, fmt.Errorf("list api key secrets: %w", err)
	}

	out := make([]ks.KeyMetadata, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, s.toMetadata(&list.Items[i]))
	}
	// Newest first, with the name as a stable tiebreaker.
	sortByCreatedDesc(out)
	return out, nil
}

func (s *Store) GetKey(ctx context.Context, owner ks.OwnerID, keyID string) (ks.KeyMetadata, error) {
	secret, err := s.getOwned(ctx, owner, keyID)
	if err != nil {
		return ks.KeyMetadata{}, err
	}
	return s.toMetadata(secret), nil
}

// getOwned fetches a Secret and confirms the caller owns it.
//
// Ownership is checked against the labels on the stored object, never against
// anything supplied by the browser. A Secret owned by someone else is reported
// as ErrNotFound, identically to a missing one, so this cannot be used to
// enumerate other people's keys.
func (s *Store) getOwned(ctx context.Context, owner ks.OwnerID, keyID string) (*corev1.Secret, error) {
	if !owner.Valid() || !validKeyID.MatchString(keyID) {
		return nil, ks.ErrNotFound
	}
	secret, err := s.secrets().Get(ctx, secretNamePrefix+keyID, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ks.ErrNotFound
		}
		return nil, fmt.Errorf("get api key secret: %w", err)
	}
	if secret.Labels[s.label(labelOwnerTID)] != owner.TenantID ||
		secret.Labels[s.label(labelOwnerOID)] != owner.ObjectID {
		return nil, ks.ErrNotFound
	}
	return secret, nil
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

	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretNamePrefix + gen.ID,
			Namespace: s.namespace,
			Labels: map[string]string{
				// The gateway selects on this label.
				s.label(labelAPIKey): "true",
				// These two are the authorization tuple.
				s.label(labelOwnerTID): owner.TenantID,
				s.label(labelOwnerOID): owner.ObjectID,
			},
			Annotations: annotations(map[string]string{
				s.label(annDisplayName):      name,
				s.label(annKeySuffix):        gen.Suffix,
				s.label(annOwnerDisplayName): owner.DisplayName,
				s.label(annOwnerEmail):       owner.Email,
				s.label(annManagedBy):        managedByValue,
			}),
		},
		Type: SecretType,
		// Keys are never edited in place. Immutability also lets the API server
		// skip watch churn, and it makes an accidental overwrite impossible.
		Immutable: &immutable,
		StringData: map[string]string{
			// The data key is the gateway's client identifier and the value is
			// the credential. The gateway's built-in API-key auth compares the
			// presented credential against these values, so the cleartext has
			// to be stored here rather than a hash.
			gen.ClientID: gen.Secret,
		},
	}

	created, err := s.secrets().Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return ks.CreatedKey{}, fmt.Errorf("create api key secret: %w", err)
	}

	return ks.CreatedKey{
		KeyMetadata: ks.KeyMetadata{
			ID:        gen.ID,
			Name:      name,
			Suffix:    gen.Suffix,
			CreatedAt: created.CreationTimestamp.Time,
		},
		Secret: gen.Secret,
	}, nil
}

func (s *Store) RevokeKey(ctx context.Context, owner ks.OwnerID, keyID string) error {
	secret, err := s.getOwned(ctx, owner, keyID)
	if err != nil {
		return err
	}
	// Delete by exact UID and resource version, so a Secret replaced between
	// the ownership check and the delete is not removed by mistake.
	precondition := metav1.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}
	err = s.secrets().Delete(ctx, secret.Name, metav1.DeleteOptions{Preconditions: &precondition})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return ks.ErrNotFound
		}
		return fmt.Errorf("delete api key secret: %w", err)
	}
	return nil
}

// Ready confirms the API server is reachable and the ServiceAccount may list
// Secrets in the namespace. Limiting to one item keeps the probe cheap.
func (s *Store) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := s.secrets().List(ctx, metav1.ListOptions{
		LabelSelector: s.label(labelAPIKey) + "=true",
		Limit:         1,
	})
	if err != nil {
		return fmt.Errorf("keystore not ready: %w", err)
	}
	return nil
}

func (s *Store) toMetadata(secret *corev1.Secret) ks.KeyMetadata {
	return ks.KeyMetadata{
		ID:        strings.TrimPrefix(secret.Name, secretNamePrefix),
		Name:      secret.Annotations[s.label(annDisplayName)],
		Suffix:    secret.Annotations[s.label(annKeySuffix)],
		CreatedAt: secret.CreationTimestamp.Time,
	}
}

// annotations drops empty values so optional fields do not appear as empty
// annotations on the stored object.
func annotations(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v != "" {
			out[k] = v
		}
	}
	return out
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
