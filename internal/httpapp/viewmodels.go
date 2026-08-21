package httpapp

import (
	"time"

	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/auth"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/brand"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/models"
	"github.com/dbirks/kubernetes-llm-api-key-portal/internal/onboarding"
)

// The types below are the contract between the Go handlers and the templates.
// They are what a template author can rely on; nothing else is in scope during
// rendering. Changing a field here is a change to that contract.

// User is the display-facing view of the signed-in person.
//
// Only the display fields appear: tenant and object IDs are authorization
// data with no business being rendered.
type User struct {
	Name     string
	Email    string
	Initials string

	// AvatarURL is the signed-in user's profile photo, or "" when there is
	// none. Templates must treat it as optional and fall back to Initials:
	// avatars are absent whenever the feature is off, the user has no photo,
	// or the photo is not in this replica's cache.
	AvatarURL string
}

// Page is embedded in every page's view model.
type Page struct {
	Brand     *brand.Brand
	Title     string
	User      *User // nil when signed out
	Flashes   []auth.Flash
	RequestID string

	// DevMode is true when the development login bypass is active. Templates
	// must render a conspicuous banner when it is set.
	DevMode bool

	// Models is true when the model catalog is configured, which gates the
	// "Models" nav link. It is independent of whether the current request could
	// list the catalog: the link shows whenever the feature is on.
	Models bool

	// ActiveNav names the navigation item base.html marks as current. It is
	// "keys", "models", "howitworks", or "" when there is nothing to highlight,
	// which is the case for the signed-out and error pages.
	ActiveNav string

	// GrafanaURL is the metrics dashboard link, or "" when none is configured.
	// When set, the header shows a "Metrics" link.
	GrafanaURL string
}

// LandingPage backs the signed-out landing page.
type LandingPage struct {
	Page
}

// HowItWorksPage backs the static explainer at GET /how-it-works.
type HowItWorksPage struct {
	Page
}

// ModelView is one model as shown on the public /models page.
type ModelView struct {
	// Name is the value to copy into the OpenAI "model" field.
	Name string

	// DisplayName is the human-facing label; it falls back to Name upstream.
	DisplayName string

	// BasePath is the base URL path this model is served under, e.g. "/v1" or
	// "/muse/v1". It is a display hint sourced from the onboarding catalog;
	// models with no configured subpath show the default "/v1".
	BasePath string

	// Status is the machine value, used only to pick the CSS class.
	Status models.Status

	// StatusLabel is the human-facing status wording.
	StatusLabel string

	// StatusClass is the status-pill modifier class for the status.
	StatusClass string
}

// ModelsPage backs the public model catalog at GET /models.
type ModelsPage struct {
	Page
	Models []ModelView

	// Enabled is false when no catalog is configured, which renders the
	// disabled explanatory state rather than a list.
	Enabled bool
}

// toModelViews maps catalog entries to their display form, attaching the
// human-facing status wording and CSS class each pill needs. basePaths maps a
// model's routing name to the base URL path it is served under; a name absent
// from the map falls back to the default "/v1".
func toModelViews(in []models.Model, basePaths map[string]string) []ModelView {
	out := make([]ModelView, 0, len(in))
	for _, m := range in {
		display := m.DisplayName
		if display == "" {
			display = m.Name
		}
		path := basePaths[m.Name]
		if path == "" {
			path = "/v1"
		}
		label, class := statusPresentation(m.Status)
		out = append(out, ModelView{
			Name:        m.Name,
			DisplayName: display,
			BasePath:    path,
			Status:      m.Status,
			StatusLabel: label,
			StatusClass: class,
		})
	}
	return out
}

// statusPresentation maps a status to its wording and pill class. The default
// arm keeps an unknown status readable rather than blank.
func statusPresentation(s models.Status) (label, class string) {
	switch s {
	case models.StatusReady:
		return "Ready", "status-ready"
	case models.StatusScaledToZero:
		return "Idle · scaled to zero", "status-idle"
	case models.StatusLoading:
		return "Loading", "status-loading"
	default:
		return "Unavailable", "status-unavailable"
	}
}

// KeyView is one API key as shown in a list.
//
// There is no field for the credential: it cannot be recovered after creation.
type KeyView struct {
	ID        string
	Name      string
	Suffix    string
	CreatedAt time.Time
}

// AccountPage backs the signed-in account page.
type AccountPage struct {
	Page
	Keys   []KeyView
	Setups []onboarding.ModelSetup

	// Catalog is the compact "endpoints you can call" summary shown on the
	// account page, independent of the live /models status list.
	Catalog []onboarding.ModelInfo

	// EnvVar is the environment variable name the guides use, so the page can
	// mention it outside a code block.
	EnvVar string

	// SelectedKeyID is the key whose detail pane is rendered. Selection is
	// resolved server-side so the page works without JavaScript.
	SelectedKeyID string
}

// NewKeyPage backs the create-key form. NameError is set when a submission was
// rejected, in which case Name carries what the user typed.
type NewKeyPage struct {
	Page
	Name      string
	NameError string
}

// CreatedKeyPage backs the one-time credential display.
//
// Secret is the only place in the entire application where a cleartext
// credential reaches a template. It must appear in the page body only: never in
// a URL, an attribute, an element id, or a data attribute.
type CreatedKeyPage struct {
	Page
	KeyName string
	Secret  string
	Setups  []onboarding.ModelSetup
	EnvVar  string
}

// RevokePage backs the revoke confirmation.
type RevokePage struct {
	Page
	Key KeyView
}

// ErrorPage backs every error response.
type ErrorPage struct {
	Page
	Heading string
	Message string
	Status  int
}

// initials derives up to two letters for an avatar-style badge.
func initials(name, email string) string {
	fields := splitName(name)
	switch len(fields) {
	case 0:
		if email != "" {
			return upperFirst(email)
		}
		return "?"
	case 1:
		return upperFirst(fields[0])
	default:
		return upperFirst(fields[0]) + upperFirst(fields[len(fields)-1])
	}
}
