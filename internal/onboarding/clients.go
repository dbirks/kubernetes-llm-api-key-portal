// Package onboarding turns deployment configuration into copy-paste setup
// instructions for each supported coding agent.
//
// Guides are data, not template branches: each client is an independent value
// that can be golden-tested on its own. When an upstream client changes its
// configuration format, the diff is confined to one function here and one
// golden file.
package onboarding

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// PlaceholderKey stands in for the credential everywhere except the one-time
// key page. It is deliberately shouty so nobody pastes it verbatim and wonders
// why authentication fails.
const PlaceholderKey = "YOUR_API_KEY"

// Params is everything a guide needs to render.
type Params struct {
	// BaseURL is the inference origin, without a trailing slash.
	BaseURL string

	// Model is the served model name to configure as the default. It is used
	// only when Models is empty, as the single-model fallback.
	Model string

	// Models is the catalog the picker offers. Each entry can sit on its own
	// base path (see Model.Path), so a client's snippets point at the right
	// URL per model. Empty falls back to a single model built from Model.
	Models []Model

	// BrandName is the operator's display name, used to label providers.
	BrandName string

	// APIKey is the credential to embed. Leave empty to use PlaceholderKey.
	APIKey string
}

// Kind classifies a model so guides can add model-appropriate advice, e.g. a
// max_tokens reminder for a model that reasons before it answers.
type Kind string

const (
	KindCoding    Kind = "coding"
	KindReasoning Kind = "reasoning"
)

// Model is one entry in the picker.
type Model struct {
	// ID is the value a client puts in the OpenAI "model" field (or Claude
	// Code's model slots) to route to this model.
	ID string

	// Label is the human-facing name shown in the picker. Falls back to ID.
	Label string

	// Kind classifies the model. Empty is treated as a plain coding model.
	Kind Kind

	// Path is the base-path segment this model is served under, before the
	// "/v1" suffix, with a leading slash and no trailing one — e.g. "" for a
	// model on the portal origin, or "/muse" for one routed under a subpath.
	Path string
}

// resolveLabel returns the display label, falling back to the ID.
func (m Model) resolveLabel() string {
	if l := strings.TrimSpace(m.Label); l != "" {
		return l
	}
	if id := strings.TrimSpace(m.ID); id != "" {
		return id
	}
	return "MODEL_NAME"
}

// GuideFile is a configuration file the user should create or edit.
type GuideFile struct {
	Path     string
	Language string
	Content  string
}

// GuideBlock is a shell snippet.
type GuideBlock struct {
	Language string
	Content  string
}

// Guide is the full setup story for one client.
type Guide struct {
	ID          string
	Name        string
	Description string
	Files       []GuideFile
	Commands    []GuideBlock
	Notes       []string

	// AgentPrompt is natural-language instructions the user can paste into a
	// coding agent to have it perform the setup.
	AgentPrompt string
}

// resolved is Params + one Model with defaults applied and derived names
// computed.
type resolved struct {
	BaseURL    string // origin + model path, e.g. https://host or https://host/muse
	APIBase    string // BaseURL + "/v1"
	Model      string
	Kind       Kind
	BrandName  string
	Key        string
	EnvVar     string // e.g. BIRKS_AI_API_KEY
	ProviderID string // e.g. birks-ai
}

// origin returns the trimmed inference origin, with a placeholder when unset so
// generated snippets never carry an empty URL.
func (p Params) origin() string {
	base := strings.TrimSuffix(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		base = "https://example.invalid"
	}
	return base
}

// models returns the catalog to render, falling back to a single model built
// from the legacy Model field when none is configured.
func (p Params) models() []Model {
	if len(p.Models) > 0 {
		return p.Models
	}
	return []Model{{ID: p.Model}}
}

func (p Params) resolve(m Model) resolved {
	base := p.origin() + strings.TrimSuffix(strings.TrimSpace(m.Path), "/")
	model := strings.TrimSpace(m.ID)
	if model == "" {
		model = "MODEL_NAME"
	}
	brand := strings.TrimSpace(p.BrandName)
	if brand == "" {
		brand = "AI Portal"
	}
	key := p.APIKey
	if key == "" {
		key = PlaceholderKey
	}
	return resolved{
		BaseURL:    base,
		APIBase:    base + "/v1",
		Model:      model,
		Kind:       m.Kind,
		BrandName:  brand,
		Key:        key,
		EnvVar:     envVarName(brand),
		ProviderID: providerID(brand),
	}
}

// envVarName derives a shell-safe environment variable from the brand name:
// "Birks AI" becomes BIRKS_AI_API_KEY.
//
// Characters outside A-Z0-9 become separators, including non-ASCII letters.
// A brand written in a non-Latin script therefore falls back to the generic
// name rather than producing something unpronounceable; that is a deliberate
// trade against pulling in a transliteration dependency.
func envVarName(brand string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(brand) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	// Trim before the emptiness check: a name of nothing but separators
	// collapses to "_", which is not empty but carries no information.
	name := strings.Trim(collapse(sb.String(), '_'), "_")
	// An empty or digit-leading name is not a valid shell identifier.
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = strings.Trim("PORTAL_"+name, "_")
	}
	return name + "_API_KEY"
}

// providerID derives a config-file-safe provider slug: "Birks AI" -> "birks-ai".
func providerID(brand string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(brand) {
		switch {
		case r >= 'a' && r <= 'z', unicode.IsDigit(r):
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	id := strings.Trim(collapse(sb.String(), '-'), "-")
	if id == "" {
		id = "portal"
	}
	return id
}

func collapse(s string, c byte) string {
	var sb strings.Builder
	var prev byte
	for i := 0; i < len(s); i++ {
		if s[i] == c && prev == c {
			continue
		}
		sb.WriteByte(s[i])
		prev = s[i]
	}
	return sb.String()
}

// jsonString quotes a value for embedding in generated JSON. Using the encoder
// rather than fmt means an operator's brand name cannot produce invalid output.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// tomlString quotes a value for generated TOML. Go's quoting rules match TOML's
// basic strings closely enough for the ASCII values involved here.
func tomlString(s string) string { return strconv.Quote(s) }

// reasoningNote is appended to every guide for a reasoning-kind model, so a
// user setting a token budget knows the reply also has to cover the model's
// hidden thinking.
const reasoningNote = "This is a reasoning model: it thinks before it answers, so leave plenty of headroom for the reply. Set a generous max_tokens (2048 or more) or the answer can be cut off mid-thought."

// guidesFor builds every client guide for one model.
//
// Claude Code comes first because it is the primary target; the generic
// OpenAI-compatible setup, Cursor, and a raw curl follow so the four most-asked
// paths lead, and the remaining coding agents come after.
func guidesFor(r resolved) []Guide {
	guides := []Guide{
		claudeCode(r),
		openaiCompatible(r),
		cursor(r),
		curlGuide(r),
		pi(r),
		opencode(r),
		codex(r),
		crush(r),
	}
	if r.Kind == KindReasoning {
		for i := range guides {
			guides[i].Notes = append(guides[i].Notes, reasoningNote)
		}
	}
	return guides
}

// Guides returns every supported client for the default (first) model. It backs
// the single-model callers and the golden tests.
func Guides(p Params) []Guide {
	return guidesFor(p.resolve(p.models()[0]))
}

// ModelSetup is the full set of client guides for one model, plus the display
// metadata the picker needs to label its model control.
type ModelSetup struct {
	// ID is a DOM-safe slug for this model, used to build panel ids.
	ID string

	// ModelID is the value a client sends in the "model" field.
	ModelID string

	// Label is the human-facing name shown on the model control.
	Label string

	// Kind classifies the model ("coding" or "reasoning").
	Kind Kind

	// APIBase is the OpenAI-style base URL for this model, shown as a hint.
	APIBase string

	Guides []Guide
}

// Setups returns the picker matrix: one entry per model, each carrying every
// client guide already parameterised for that model's base URL.
func Setups(p Params) []ModelSetup {
	models := p.models()
	out := make([]ModelSetup, 0, len(models))
	for _, m := range models {
		r := p.resolve(m)
		out = append(out, ModelSetup{
			ID:      slug(r.Model),
			ModelID: r.Model,
			Label:   m.resolveLabel(),
			Kind:    m.Kind,
			APIBase: r.APIBase,
			Guides:  guidesFor(r),
		})
	}
	return out
}

// ModelInfo is a compact, display-facing description of one servable model,
// used by the account page's endpoints summary.
type ModelInfo struct {
	ID      string
	Label   string
	Kind    Kind
	APIBase string
}

// Catalog returns the configured models with their resolved base URLs, for
// pages that show "what you can call and where" outside the full picker.
func Catalog(p Params) []ModelInfo {
	models := p.models()
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		r := p.resolve(m)
		out = append(out, ModelInfo{
			ID:      r.Model,
			Label:   m.resolveLabel(),
			Kind:    m.Kind,
			APIBase: r.APIBase,
		})
	}
	return out
}

// EnvVar returns the environment variable name the guides use for the
// credential, so pages can refer to it in prose.
func EnvVar(p Params) string { return p.resolve(p.models()[0]).EnvVar }

// ProviderID returns the config-file slug the guides use to name the provider.
func ProviderID(p Params) string { return p.resolve(p.models()[0]).ProviderID }

// GuideByID returns a single guide for the default model, and whether it existed.
func GuideByID(p Params, id string) (Guide, bool) {
	for _, g := range Guides(p) {
		if g.ID == id {
			return g, true
		}
	}
	return Guide{}, false
}

// slug turns a model name into a DOM-safe token for element ids.
func slug(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	id := strings.Trim(collapse(sb.String(), '-'), "-")
	if id == "" {
		id = "model"
	}
	return id
}
