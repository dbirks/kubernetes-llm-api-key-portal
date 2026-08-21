package models

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newFakeCatalog builds a Store backed by the fake dynamic client seeded with
// the given objects. The custom list kind is registered so the fake can answer
// a List for our CRD's GVR, which it cannot infer for an unstructured type.
func newFakeCatalog(t *testing.T, namespace, selector string, objs ...*unstructured.Unstructured) *Store {
	t.Helper()

	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		GVR:           "LLMInferenceServiceList",
		deploymentGVR: "DeploymentList",
	}
	runtimeObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, runtimeObjs...)

	store, err := New(Options{Client: client, Namespace: namespace, Selector: selector})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

// llmisvc constructs one LLMInferenceService object. modelName is the routed
// name (empty falls back to metadata.name); ready is the Ready condition
// status (empty omits the condition); replicas is the desired replica count
// (nil omits it).
func llmisvc(name, modelName, ready string, replicas *int64, labels map[string]string) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": GVR.GroupVersion().String(),
		"kind":       "LLMInferenceService",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "models",
		},
		"spec":   map[string]any{},
		"status": map[string]any{},
	}
	if labels != nil {
		lbl := map[string]any{}
		for k, v := range labels {
			lbl[k] = v
		}
		obj["metadata"].(map[string]any)["labels"] = lbl
	}
	spec := obj["spec"].(map[string]any)
	if modelName != "" {
		spec["model"] = map[string]any{"name": modelName}
	}
	if replicas != nil {
		spec["replicas"] = *replicas
	}
	if ready != "" {
		obj["status"].(map[string]any)["conditions"] = []any{
			map[string]any{"type": "Ready", "status": ready},
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

// deployment constructs an engine Deployment object. spec is the desired
// replica count (nil omits spec.replicas entirely); ready is status.readyReplicas
// (0 omits it, matching how the field is absent until a pod is ready).
func deployment(name string, spec *int64, ready int64) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": deploymentGVR.GroupVersion().String(),
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "models",
		},
		"spec":   map[string]any{},
		"status": map[string]any{},
	}
	if spec != nil {
		obj["spec"].(map[string]any)["replicas"] = *spec
	}
	if ready > 0 {
		obj["status"].(map[string]any)["readyReplicas"] = ready
	}
	return &unstructured.Unstructured{Object: obj}
}

func int64p(v int64) *int64 { return &v }

func findModel(models []Model, name string) (Model, bool) {
	for _, m := range models {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

// Status is derived from the engine Deployment (<isvc>-kserve), never from the
// LLMInferenceService's Ready condition — which stays True even at zero
// replicas. Each case seeds the LLMISVC with Ready=True on purpose, to prove
// that condition is ignored and the Deployment's replica counts win.
func TestListMapsStatus(t *testing.T) {
	tests := []struct {
		name     string
		isvc     *unstructured.Unstructured
		dep      *unstructured.Unstructured
		wantName string
		want     Status
	}{
		{
			name:     "ready when the engine has a ready replica",
			isvc:     llmisvc("qwen", "qwen3.8-nvfp4", "True", int64p(0), nil),
			dep:      deployment("qwen-kserve", int64p(1), 1),
			wantName: "qwen3.8-nvfp4",
			want:     StatusReady,
		},
		{
			name:     "idle when the engine is scaled to zero, despite Ready=True",
			isvc:     llmisvc("muse", "muse-glimmer-30b", "True", int64p(1), nil),
			dep:      deployment("muse-kserve", int64p(0), 0),
			wantName: "muse-glimmer-30b",
			want:     StatusScaledToZero,
		},
		{
			name:     "loading when desired exceeds ready",
			isvc:     llmisvc("cold", "cold-model", "True", nil, nil),
			dep:      deployment("cold-kserve", int64p(1), 0),
			wantName: "cold-model",
			want:     StatusLoading,
		},
		{
			name:     "unknown when the engine Deployment is missing",
			isvc:     llmisvc("gone", "gone-model", "True", int64p(1), nil),
			dep:      nil,
			wantName: "gone-model",
			want:     StatusUnknown,
		},
		{
			name:     "unknown when the Deployment omits spec.replicas",
			isvc:     llmisvc("mystery", "mystery-model", "True", int64p(1), nil),
			dep:      deployment("mystery-kserve", nil, 0),
			wantName: "mystery-model",
			want:     StatusUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []*unstructured.Unstructured{tc.isvc}
			if tc.dep != nil {
				objs = append(objs, tc.dep)
			}
			store := newFakeCatalog(t, "models", "", objs...)
			got, err := store.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("List returned %d models, want 1", len(got))
			}
			m := got[0]
			if m.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", m.Name, tc.wantName)
			}
			if m.Status != tc.want {
				t.Errorf("Status = %q, want %q", m.Status, tc.want)
			}
		})
	}
}

// The routed name falls back to the object's own name when spec.model.name is
// absent, which is what KServe defaults the served name to.
func TestNameFallsBackToMetadataName(t *testing.T) {
	store := newFakeCatalog(t, "models", "", llmisvc("bare-name", "", "True", int64p(1), nil))
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "bare-name" {
		t.Fatalf("got %+v, want a single model named bare-name", got)
	}
}

// DisplayName defaults to the routed name unless a friendly annotation is set.
func TestDisplayNameFromAnnotation(t *testing.T) {
	obj := llmisvc("qwen", "qwen3.8-nvfp4", "True", int64p(1), nil)
	obj.SetAnnotations(map[string]string{"serving.kserve.io/displayName": "Qwen 3.8 27B"})
	store := newFakeCatalog(t, "models", "", obj)

	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].DisplayName != "Qwen 3.8 27B" {
		t.Errorf("DisplayName = %q, want the friendly annotation", got[0].DisplayName)
	}

	plain := newFakeCatalog(t, "models", "", llmisvc("q", "qwen3.8-nvfp4", "True", int64p(1), nil))
	list, err := plain.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list[0].DisplayName != "qwen3.8-nvfp4" {
		t.Errorf("DisplayName = %q, want it to default to the routed name", list[0].DisplayName)
	}
}

// The result is sorted by display name so the page is stable regardless of API
// server ordering.
func TestListSortsAndReturnsAll(t *testing.T) {
	store := newFakeCatalog(t, "models", "",
		llmisvc("c", "zeta", "True", int64p(1), nil),
		llmisvc("a", "alpha", "True", int64p(1), nil),
		llmisvc("b", "mu", "True", int64p(1), nil),
	)
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d models, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("model[%d] = %q, want %q", i, got[i].Name, name)
		}
	}
}

// A label selector filters the catalog on the API server side.
func TestListHonoursSelector(t *testing.T) {
	store := newFakeCatalog(t, "models", "tier=public",
		llmisvc("pub", "public-model", "True", int64p(1), map[string]string{"tier": "public"}),
		llmisvc("priv", "private-model", "True", int64p(1), map[string]string{"tier": "internal"}),
	)
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := findModel(got, "public-model"); !ok {
		t.Error("the selected model is missing")
	}
	if _, ok := findModel(got, "private-model"); ok {
		t.Error("a model outside the selector was returned")
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{Namespace: "models"}); err == nil {
		t.Error("New accepted a nil client")
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{GVR: "LLMInferenceServiceList"})
	if _, err := New(Options{Client: client}); err == nil {
		t.Error("New accepted an empty namespace")
	}
}
