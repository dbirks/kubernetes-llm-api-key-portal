// Package models reads the inference catalog the gateway routes to.
//
// The portal deliberately holds no API key and never calls the inference
// endpoint's /v1/* surface, which is auth-gated. So the list of servable
// models cannot come from /v1/models. It is read instead from the cluster:
// each model is a KServe LLMInferenceService object, and this package turns
// those into a small, display-facing catalog.
//
// This is read-only and advisory. Nothing here enforces anything; key
// enforcement remains the gateway's job.
package models

import "context"

// Status is the coarse serving state of one model, as far as the portal can
// tell from the object it reads. It is intentionally small: the portal shows a
// hint, not an operational dashboard.
type Status string

const (
	// StatusReady means the model is serving and a request will be answered
	// without a cold start.
	StatusReady Status = "ready"

	// StatusScaledToZero means the model is available but idle: it has been
	// scaled to zero replicas, so the first request loads it on demand and
	// pays a cold start.
	StatusScaledToZero Status = "scaled-to-zero"

	// StatusLoading means the model is not ready yet — spinning up, pulling
	// weights, or otherwise in transition.
	StatusLoading Status = "loading"

	// StatusUnavailable means the object exists but its state could not be
	// determined. It is the safe answer when the shape is unfamiliar.
	StatusUnavailable Status = "unavailable"
)

// Model is one catalog entry.
type Model struct {
	// Name is the value a client puts in the OpenAI "model" field to route to
	// this model through the gateway.
	Name string

	// DisplayName is a human-facing label. It falls back to Name.
	DisplayName string

	// Status is the model's coarse serving state.
	Status Status
}

// Catalog lists the models the gateway can route to.
//
// It is an interface so the web layer depends on the capability and not on the
// Kubernetes client behind it, matching how the app already treats the keystore
// and the auth provider. A nil Catalog means the feature is off.
type Catalog interface {
	List(ctx context.Context) ([]Model, error)
}
