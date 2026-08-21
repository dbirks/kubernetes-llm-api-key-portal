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
	want := []string{"claude-code", "openai", "cursor", "curl", "pi", "opencode", "codex", "crush"}

	if len(guides) != len(want) {
		t.Fatalf("got %d guides, want %d", len(guides), len(want))
	}
	for i, id := range want {
		if guides[i].ID != id {
			t.Errorf("guide %d = %q, want %q", i, guides[i].ID, id)
		}
	}
}

// A model served under a base path must have that path in the URLs its guides
// generate, so a picker selection actually points at the right endpoint.
func TestModelPathIsAppliedToBaseURL(t *testing.T) {
	p := Params{
		BaseURL:   "https://llm.birks.dev",
		BrandName: "llm.birks.dev",
		Models: []Model{
			{ID: "qwen3.8-nvfp4", Label: "Qwen", Kind: KindCoding, Path: ""},
			{ID: "muse-glimmer-30b", Label: "Muse", Kind: KindReasoning, Path: "/muse"},
		},
	}
	setups := Setups(p)
	if len(setups) != 2 {
		t.Fatalf("got %d setups, want 2", len(setups))
	}
	if setups[0].APIBase != "https://llm.birks.dev/v1" {
		t.Errorf("qwen APIBase = %q", setups[0].APIBase)
	}
	if setups[1].APIBase != "https://llm.birks.dev/muse/v1" {
		t.Errorf("muse APIBase = %q", setups[1].APIBase)
	}
	// Every OpenAI-style muse guide must route under the /muse path, and every
	// guide must carry the reasoning note. Claude Code is the exception: it uses
	// the shared Anthropic surface at /anthropic for every model, routing by the
	// request body's model field rather than a per-model base path.
	for _, g := range setups[1].Guides {
		text := render(g)
		if g.ID == "claude-code" {
			if !strings.Contains(text, "llm.birks.dev/anthropic") {
				t.Errorf("claude-code guide does not use the /anthropic surface:\n%s", text)
			}
			if strings.Contains(text, "llm.birks.dev/muse") {
				t.Errorf("claude-code guide must not carry the per-model /muse path:\n%s", text)
			}
		} else if !strings.Contains(text, "llm.birks.dev/muse") {
			t.Errorf("muse guide %q does not route under /muse:\n%s", g.ID, text)
		}
		if !strings.Contains(text, "reasoning model") {
			t.Errorf("muse guide %q lacks the reasoning-model note", g.ID)
		}
	}
	// Coding-model guides must not carry the reasoning note.
	for _, g := range setups[0].Guides {
		if strings.Contains(render(g), "reasoning model") {
			t.Errorf("coding guide %q wrongly carries the reasoning note", g.ID)
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

// blocksByShell splits a guide's command blocks by their Shell tag.
func blocksByShell(g Guide) (sh, ps, agnostic []GuideBlock) {
	for _, b := range g.Commands {
		switch b.Shell {
		case shellSh:
			sh = append(sh, b)
		case shellPS:
			ps = append(ps, b)
		default:
			agnostic = append(agnostic, b)
		}
	}
	return
}

// Env-based clients (no config file) must offer both an sh and a PowerShell
// command block so the account page's shell toggle has something to swap.
func TestEnvBasedGuidesHaveBothShells(t *testing.T) {
	guides := map[string]Guide{}
	for _, g := range Guides(testParams()) {
		guides[g.ID] = g
	}
	for _, id := range []string{"claude-code", "openai", "curl"} {
		g := guides[id]
		if len(g.Files) != 0 {
			t.Errorf("%s: expected an env-based guide with no config file, got %d files", id, len(g.Files))
		}
		sh, ps, _ := blocksByShell(g)
		if len(sh) != 1 || len(ps) != 1 {
			t.Errorf("%s: want exactly one sh and one PowerShell block, got sh=%d ps=%d", id, len(sh), len(ps))
		}
	}
}

// Config-file clients must ship the file contents shell-agnostically (so it is
// never hidden by the shell toggle) plus one sh and one PowerShell block that
// export the credential and launch the tool.
func TestConfigFileGuidesHaveFilePlusPerShellExport(t *testing.T) {
	guides := map[string]Guide{}
	for _, g := range Guides(testParams()) {
		guides[g.ID] = g
	}
	for _, id := range []string{"codex", "opencode", "crush", "pi"} {
		g := guides[id]
		if len(g.Files) == 0 {
			t.Errorf("%s: expected a config file", id)
		}
		sh, ps, _ := blocksByShell(g)
		if len(sh) != 1 || len(ps) != 1 {
			t.Errorf("%s: want one sh and one PowerShell export block, got sh=%d ps=%d", id, len(sh), len(ps))
		}
	}
}

// The PowerShell blocks must actually use PowerShell syntax, not sh syntax, or
// they will not run in the shell the toggle claims to target.
func TestPowerShellBlocksUsePowerShellSyntax(t *testing.T) {
	for _, g := range Guides(testParams()) {
		_, ps, _ := blocksByShell(g)
		for _, b := range ps {
			if strings.Contains(b.Content, "export ") {
				t.Errorf("%s PowerShell block uses sh `export`:\n%s", g.ID, b.Content)
			}
			if !strings.Contains(b.Content, "$env:") {
				t.Errorf("%s PowerShell block never sets $env::\n%s", g.ID, b.Content)
			}
		}
	}
}

// Claude Code must use the Anthropic bearer token variable and all three model
// slots, and must not leak a real Anthropic model via a missing HAIKU slot or a
// deprecated variable. This is high-risk correction #2 from the reference.
func TestClaudeCodeAuthAndModelSlots(t *testing.T) {
	g, ok := GuideByID(testParams(), "claude-code")
	if !ok {
		t.Fatal("claude-code guide missing")
	}
	text := render(g)
	for _, want := range []string{
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("claude-code guide is missing %s", want)
		}
	}
	if strings.Contains(text, "ANTHROPIC_API_KEY=") {
		t.Error("claude-code guide sets ANTHROPIC_API_KEY, which triggers the x-api-key approval prompt; use ANTHROPIC_AUTH_TOKEN")
	}
	if strings.Contains(text, "ANTHROPIC_SMALL_FAST_MODEL") {
		t.Error("claude-code guide uses the deprecated ANTHROPIC_SMALL_FAST_MODEL")
	}
	// The Anthropic base URL must not carry a /v1 suffix; Claude Code appends
	// /v1/messages itself.
	if strings.Contains(text, "ANTHROPIC_BASE_URL=\"https://llm.birks.dev/v1\"") {
		t.Error("claude-code ANTHROPIC_BASE_URL must not include /v1")
	}
	// It must point at the gateway's Anthropic surface at /anthropic (the path
	// that triggers translation), not the bare origin.
	if !strings.Contains(text, "ANTHROPIC_BASE_URL=\"https://llm.birks.dev/anthropic\"") {
		t.Errorf("claude-code ANTHROPIC_BASE_URL must be the /anthropic surface:\n%s", text)
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
