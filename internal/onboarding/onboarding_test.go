package onboarding

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the golden files. Run with:
//
//	go test ./internal/onboarding -update
//
// Snippets are golden-tested so that an upstream client changing its config
// format shows up as a reviewable diff in a pull request rather than as a
// silently wrong instruction on the account page.
var update = flag.Bool("update", false, "rewrite golden files")

func testParams() Params {
	return Params{
		BaseURL:   "https://llm.birks.dev",
		Model:     "Qwen3-Coder-30B",
		BrandName: "llm.birks.dev",
	}
}

// render produces a stable text rendering of a guide for golden comparison.
func render(g Guide) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "id: %s\nname: %s\n\n%s\n", g.ID, g.Name, g.Description)
	for _, f := range g.Files {
		fmt.Fprintf(&sb, "\n--- file %s (%s) ---\n%s\n", f.Path, f.Language, f.Content)
	}
	for _, c := range g.Commands {
		fmt.Fprintf(&sb, "\n--- commands (%s) ---\n%s\n", c.Language, c.Content)
	}
	if len(g.Notes) > 0 {
		sb.WriteString("\n--- notes ---\n")
		for _, n := range g.Notes {
			fmt.Fprintf(&sb, "- %s\n", n)
		}
	}
	fmt.Fprintf(&sb, "\n--- agent prompt ---\n%s\n", g.AgentPrompt)
	return sb.String()
}

func TestGuidesMatchGolden(t *testing.T) {
	for _, guide := range Guides(testParams()) {
		t.Run(guide.ID, func(t *testing.T) {
			path := filepath.Join("testdata", guide.ID+".golden")
			got := render(guide)

			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if got != string(want) {
				t.Errorf("guide %s differs from its golden file.\n\ngot:\n%s\n\nwant:\n%s", guide.ID, got, want)
			}
		})
	}
}

func TestAllClientsArePresent(t *testing.T) {
	guides := Guides(testParams())
	want := []string{"claude-code", "pi", "opencode", "codex", "crush"}

	if len(guides) != len(want) {
		t.Fatalf("got %d guides, want %d", len(guides), len(want))
	}
	for i, id := range want {
		if guides[i].ID != id {
			t.Errorf("guide %d = %q, want %q", i, guides[i].ID, id)
		}
	}
}

// Every guide must be complete enough to act on.
func TestGuidesAreWellFormed(t *testing.T) {
	for _, g := range Guides(testParams()) {
		t.Run(g.ID, func(t *testing.T) {
			if g.Name == "" || g.Description == "" || g.AgentPrompt == "" {
				t.Error("guide is missing a name, description, or agent prompt")
			}
			if len(g.Files) == 0 && len(g.Commands) == 0 {
				t.Error("guide has nothing to copy")
			}
			// The endpoint has to appear somewhere the user can see it.
			if !strings.Contains(render(g), "llm.birks.dev") {
				t.Error("guide never mentions the endpoint")
			}
		})
	}
}

// Generated JSON must actually parse, whatever the operator called their brand.
func TestGeneratedJSONIsValid(t *testing.T) {
	params := testParams()
	params.BrandName = `Acme "Quote" \ Co`

	for _, g := range Guides(params) {
		for _, f := range g.Files {
			if f.Language != "json" {
				continue
			}
			// The schema line is a real JSON key, so the whole file should parse.
			var out map[string]any
			if err := json.Unmarshal([]byte(f.Content), &out); err != nil {
				t.Errorf("%s: %s is not valid JSON: %v\n%s", g.ID, f.Path, err, f.Content)
			}
		}
	}
}

func TestEnvVarDerivation(t *testing.T) {
	tests := map[string]string{
		"Birks AI":    "BIRKS_AI_API_KEY",
		"Acme":        "ACME_API_KEY",
		"Acme  Corp.": "ACME_CORP_API_KEY",
		"acme-corp":   "ACME_CORP_API_KEY",
		"7 Bridges":   "PORTAL_7_BRIDGES_API_KEY",
		// Non-ASCII letters are separators, so a purely non-Latin name falls
		// back to the generic identifier.
		"日本":          "PORTAL_API_KEY",
		"Ünïcodé Ltd": "N_COD_LTD_API_KEY",
	}
	for brand, want := range tests {
		if got := envVarName(brand); got != want {
			t.Errorf("envVarName(%q) = %q, want %q", brand, got, want)
		}
	}
}

// The derived variable must be a usable shell identifier for every input.
func TestEnvVarIsAlwaysAValidIdentifier(t *testing.T) {
	for _, brand := range []string{"Birks AI", "!!!", "日本", "7 Bridges", "a", strings.Repeat("x", 64)} {
		got := envVarName(brand)
		if got == "" {
			t.Errorf("envVarName(%q) is empty", brand)
			continue
		}
		if got[0] >= '0' && got[0] <= '9' {
			t.Errorf("envVarName(%q) = %q starts with a digit", brand, got)
		}
		for _, r := range got {
			valid := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
			if !valid {
				t.Errorf("envVarName(%q) = %q contains %q", brand, got, r)
				break
			}
		}
	}
}

func TestProviderIDDerivation(t *testing.T) {
	tests := map[string]string{
		"Birks AI":  "birks-ai",
		"Acme Corp": "acme-corp",
		"!!!":       "portal",
		"  ":        "portal",
	}
	for brand, want := range tests {
		if got := providerID(brand); got != want {
			t.Errorf("providerID(%q) = %q, want %q", brand, got, want)
		}
	}
}

// Without an explicit key, guides must carry the placeholder and never an empty
// string, which would look like a working config.
func TestPlaceholderIsUsedWhenNoKeyIsGiven(t *testing.T) {
	for _, g := range Guides(testParams()) {
		text := render(g)
		if !strings.Contains(text, PlaceholderKey) {
			t.Errorf("%s does not use the placeholder credential", g.ID)
		}
	}
}

func TestRealKeyIsSubstituted(t *testing.T) {
	params := testParams()
	params.APIKey = "llm_realcredentialvalue"

	for _, g := range Guides(params) {
		text := render(g)
		if !strings.Contains(text, params.APIKey) {
			t.Errorf("%s does not include the supplied credential", g.ID)
		}
		if strings.Contains(text, PlaceholderKey) {
			t.Errorf("%s still shows the placeholder alongside a real credential", g.ID)
		}
	}
}

// The agent prompt is pasted into another coding agent, so it must tell that
// agent not to commit or print the credential.
func TestAgentPromptWarnsAboutTheCredential(t *testing.T) {
	for _, g := range Guides(testParams()) {
		if !strings.Contains(g.AgentPrompt, "Do not write the key itself into any file in a git repository") {
			t.Errorf("%s agent prompt lacks the do-not-commit instruction", g.ID)
		}
	}
}

func TestGuideByID(t *testing.T) {
	if _, ok := GuideByID(testParams(), "claude-code"); !ok {
		t.Error("GuideByID did not find claude-code")
	}
	if _, ok := GuideByID(testParams(), "nope"); ok {
		t.Error("GuideByID found a guide that does not exist")
	}
}

// Zero-value params must still render something coherent rather than a config
// with empty URLs a user might paste.
func TestEmptyParamsDegradeSafely(t *testing.T) {
	for _, g := range Guides(Params{}) {
		text := render(g)
		if strings.Contains(text, `""`) && strings.Contains(text, "base_url") {
			t.Errorf("%s produced an empty base URL", g.ID)
		}
		if !strings.Contains(text, "MODEL_NAME") {
			t.Errorf("%s does not fall back to a visible model placeholder", g.ID)
		}
	}
}
