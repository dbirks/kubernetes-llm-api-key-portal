package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Group and Resource are fixed by KServe's API. Version is the served version
// our LLMInferenceService CRs use.
//
// It is a single named constant on purpose: if KServe promotes the API (for
// example to v1beta1), this is the one line to change. The List path treats a
// "no matches for kind" outcome as a normal error rather than a panic, so a
// version skew degrades to a friendly failure the page can show, not a crash —
// which is the defensive posture we want without reading the CRD directly
// (that would need cluster-scoped RBAC the portal is deliberately not granted).
const (
	catalogGroup    = "serving.kserve.io"
	catalogVersion  = "v1alpha2"
	catalogResource = "llminferenceservices"
)

// listTimeout bounds the catalog read so a slow API server cannot hang the page.
const listTimeout = 5 * time.Second

// GVR is the resource this store lists.
var GVR = schema.GroupVersionResource{
	Group:    catalogGroup,
	Version:  catalogVersion,
	Resource: catalogResource,
}

// Options configures the Kubernetes catalog.
type Options struct {
	// Client is a dynamic client. Injectable so tests can use the fake.
	Client dynamic.Interface

	// Namespace holds the LLMInferenceService objects.
	Namespace string

	// Selector is an optional label selector, e.g. to show only a curated
	// subset of the models in the namespace. Empty lists them all.
	Selector string
}

// Store lists LLMInferenceService objects and maps them to catalog entries.
type Store struct {
	client    dynamic.Interface
	namespace string
	selector  string
}

// New validates options and returns a Store.
func New(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("dynamic client is required")
	}
	if opts.Namespace == "" {
		return nil, errors.New("namespace is required")
	}
	return &Store{
		client:    opts.Client,
		namespace: opts.Namespace,
		selector:  opts.Selector,
	}, nil
}

// List reads the catalog. The result is sorted by display name so the page is
// stable across calls regardless of API server ordering.
func (s *Store) List(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	list, err := s.client.Resource(GVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: s.selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", catalogResource, err)
	}

	out := make([]Model, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toModel(&list.Items[i]))
	}
	sortByDisplayName(out)
	return out, nil
}

// toModel maps one object to a catalog entry.
//
// The routing name is spec.model.name, which is what a client sends in the
// OpenAI "model" field. It falls back to the object's own name, which is what
// KServe defaults the served name to when the field is omitted.
func toModel(obj *unstructured.Unstructured) Model {
	name, ok, _ := unstructured.NestedString(obj.Object, "spec", "model", "name")
	if !ok || name == "" {
		name = obj.GetName()
	}
	display := name
	if friendly := obj.GetAnnotations()["serving.kserve.io/displayName"]; friendly != "" {
		display = friendly
	}
	return Model{
		Name:        name,
		DisplayName: display,
		Status:      deriveStatus(obj),
	}
}

// deriveStatus reads a coarse serving state off the object.
//
// This is the one place that knows the object's status shape, kept isolated on
// purpose. The scaled-to-zero-vs-loading distinction is KServe-version-specific
// and the field it reads may move between releases; when it cannot be told
// apart, the safe two-state answer (ready vs not-ready) is what falls out. Only
// the presentation of that hint depends on getting the nuance exactly right, so
// a wrong guess here is a cosmetic label, never a routing or auth decision.
func deriveStatus(obj *unstructured.Unstructured) Status {
	// An explicit desired-zero is the clearest signal of a deliberately idle
	// model, so it wins over the Ready condition: a scaled-to-zero model can
	// still report Ready=True on the last-known revision.
	if desired, ok := desiredReplicas(obj); ok && desired == 0 {
		return StatusScaledToZero
	}

	switch readyCondition(obj) {
	case "True":
		return StatusReady
	case "False", "Unknown":
		return StatusLoading
	default:
		// No Ready condition and no replica hint: we genuinely cannot tell.
		return StatusUnavailable
	}
}

// readyCondition returns the status of the Ready condition ("True", "False",
// "Unknown"), or "" when there is no such condition.
func readyCondition(obj *unstructured.Unstructured) string {
	conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !ok {
		return ""
	}
	for _, c := range conds {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cond["type"].(string); t == "Ready" {
			s, _ := cond["status"].(string)
			return s
		}
	}
	return ""
}

// desiredReplicas returns the desired replica count and whether one was found.
//
// The field's home is version-specific, so a few known locations are tried in
// order. A missing value is reported as not-found rather than as zero, so an
// object that simply does not expose it is not mistaken for scaled-to-zero.
func desiredReplicas(obj *unstructured.Unstructured) (int64, bool) {
	for _, path := range [][]string{
		{"spec", "replicas"},
		{"status", "replicas"},
	} {
		if v, ok, err := unstructured.NestedInt64(obj.Object, path...); ok && err == nil {
			return v, true
		}
	}
	return 0, false
}

func sortByDisplayName(m []Model) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0; j-- {
			a, b := m[j-1], m[j]
			less := b.DisplayName < a.DisplayName ||
				(b.DisplayName == a.DisplayName && b.Name < a.Name)
			if !less {
				break
			}
			m[j-1], m[j] = b, a
		}
	}
}

var _ Catalog = (*Store)(nil)
