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

	// Model is the served model name to configure as the default.
	Model string

	// BrandName is the operator's display name, used to label providers.
	BrandName string

	// APIKey is the credential to embed. Leave empty to use PlaceholderKey.
	APIKey string
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

// resolved is Params with defaults applied and derived names computed.
type resolved struct {
	BaseURL    string
	APIBase    string // BaseURL + "/v1"
	Model      string
	BrandName  string
	Key        string
	EnvVar     string // e.g. BIRKS_AI_API_KEY
	ProviderID string // e.g. birks-ai
}

func (p Params) resolve() resolved {
	base := strings.TrimSuffix(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		base = "https://example.invalid"
	}
	model := strings.TrimSpace(p.Model)
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

// Guides returns every supported client, in the order they should be shown.
//
// Claude Code comes first because it is the primary target; the rest follow the
// order in the design brief.
func Guides(p Params) []Guide {
	r := p.resolve()
	return []Guide{
		claudeCode(r),
		pi(r),
		opencode(r),
		codex(r),
		crush(r),
	}
}

// EnvVar returns the environment variable name the guides use for the
// credential, so pages can refer to it in prose.
func EnvVar(p Params) string { return p.resolve().EnvVar }

// ProviderID returns the config-file slug the guides use to name the provider.
func ProviderID(p Params) string { return p.resolve().ProviderID }

// GuideByID returns a single guide, and whether it existed.
func GuideByID(p Params, id string) (Guide, bool) {
	for _, g := range Guides(p) {
		if g.ID == id {
			return g, true
		}
	}
	return Guide{}, false
}
