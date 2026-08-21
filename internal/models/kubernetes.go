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

// engineSuffix is appended to an LLMInferenceService's name to find the engine
// Deployment KServe creates for it, e.g. "qwen38-llm" -> "qwen38-llm-kserve".
// That Deployment — not the LLMISVC's Ready condition — is the source of truth
// for serving state, because the Ready condition stays True even at zero
// replicas under scale-to-zero.
const engineSuffix = "-kserve"

// listTimeout bounds the catalog read so a slow API server cannot hang the page.
const listTimeout = 5 * time.Second

// GVR is the resource this store lists.
var GVR = schema.GroupVersionResource{
	Group:    catalogGroup,
	Version:  catalogVersion,
	Resource: catalogResource,
}

// deploymentGVR is the engine Deployment resource the status derivation reads.
var deploymentGVR = schema.GroupVersionResource{
	Group:    "apps",
	Version:  "v1",
	Resource: "deployments",
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

	// Read the engine Deployments once and index them by name. A failure here is
	// not fatal: every model then derives StatusUnknown, which is the honest
	// answer when we cannot verify serving state — never a false "Ready".
	deploys := s.engineDeployments(ctx)

	out := make([]Model, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toModel(&list.Items[i], deploys))
	}
	sortByDisplayName(out)
	return out, nil
}

// engineDeployments lists the Deployments in the catalog namespace and indexes
// them by name. It returns nil on any error; callers treat a missing entry and
// a nil map identically, so a read failure degrades every model to Unknown
// rather than failing the page.
func (s *Store) engineDeployments(ctx context.Context) map[string]*unstructured.Unstructured {
	list, err := s.client.Resource(deploymentGVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	byName := make(map[string]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		byName[list.Items[i].GetName()] = &list.Items[i]
	}
	return byName
}

// toModel maps one object to a catalog entry.
//
// The routing name is spec.model.name, which is what a client sends in the
// OpenAI "model" field. It falls back to the object's own name, which is what
// KServe defaults the served name to when the field is omitted.
func toModel(obj *unstructured.Unstructured, deploys map[string]*unstructured.Unstructured) Model {
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
		Status:      deriveStatus(deploys[obj.GetName()+engineSuffix]),
	}
}

// deriveStatus reads a coarse serving state off the engine Deployment.
//
// The LLMInferenceService's own Ready condition is deliberately NOT consulted:
// it stays True even at zero replicas under scale-to-zero, so every idle model
// would misreport as Ready. The Deployment's replica counts are the truth.
//
//   - no Deployment, or no spec.replicas to read -> Unknown (we cannot verify;
//     never assume Ready)
//   - spec.replicas == 0                         -> Idle · scaled to zero
//   - spec.replicas > 0 and readyReplicas < it   -> Loading
//   - otherwise (readyReplicas >= spec.replicas) -> Ready
func deriveStatus(dep *unstructured.Unstructured) Status {
	if dep == nil {
		return StatusUnknown
	}
	desired, ok, err := unstructured.NestedInt64(dep.Object, "spec", "replicas")
	if !ok || err != nil {
		return StatusUnknown
	}
	if desired == 0 {
		return StatusScaledToZero
	}
	// readyReplicas is omitted from status until at least one pod is ready, so a
	// missing value reads as zero — which is correct: nothing is ready yet.
	ready, _, _ := unstructured.NestedInt64(dep.Object, "status", "readyReplicas")
	if ready < desired {
		return StatusLoading
	}
	return StatusReady
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
