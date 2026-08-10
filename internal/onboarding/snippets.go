package onboarding

import (
	"fmt"
	"strings"
)

// The snippets below were written against upstream documentation as of August
// 2026. Client configuration formats move faster than this portal does, so each
// one is covered by a golden test: when a format changes, update the builder
// here and run `go test ./internal/onboarding -update` to review the diff.

// exportLine renders a shell export, quoting the value.
func exportLine(name, value string) string {
	return fmt.Sprintf("export %s=%q", name, value)
}

// keyExport is the line that puts the credential in the environment. Every
// guide leads with it, so the credential lives in exactly one place per client
// rather than being pasted into a config file that might get committed.
func keyExport(r resolved) string { return exportLine(r.EnvVar, r.Key) }

func claudeCode(r resolved) Guide {
	commands := strings.Join([]string{
		keyExport(r),
		"",
		exportLine("ANTHROPIC_BASE_URL", r.BaseURL),
		fmt.Sprintf("export ANTHROPIC_AUTH_TOKEN=\"$%s\"", r.EnvVar),
		exportLine("ANTHROPIC_DEFAULT_OPUS_MODEL", r.Model),
		exportLine("ANTHROPIC_DEFAULT_SONNET_MODEL", r.Model),
		exportLine("ANTHROPIC_DEFAULT_HAIKU_MODEL", r.Model),
		"",
		"claude",
	}, "\n")

	return Guide{
		ID:          "claude-code",
		Name:        "Claude Code",
		Description: fmt.Sprintf("Point Claude Code at %s using gateway bearer authentication. The endpoint implements the Anthropic Messages API, so no translation layer is needed.", r.BrandName),
		Commands:    []GuideBlock{{Language: "bash", Content: commands}},
		Notes: []string{
			"Add the exports to your shell profile to make them permanent.",
			"The three model variables map Claude Code's Opus, Sonnet, and Haiku slots onto the single served model, so every mode routes to the same place.",
			"ANTHROPIC_AUTH_TOKEN is the gateway bearer token. Leave ANTHROPIC_API_KEY unset so it cannot take precedence.",
		},
		AgentPrompt: agentPrompt(r, "Claude Code",
			"Set ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, and the ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL variables in my shell profile."),
	}
}

func pi(r resolved) Guide {
	// Built with the JSON encoder so an operator brand name containing quotes
	// cannot produce a broken config file.
	content := strings.Join([]string{
		"{",
		"  \"providers\": {",
		"    " + jsonString(r.ProviderID) + ": {",
		"      \"baseUrl\": " + jsonString(r.APIBase) + ",",
		"      \"api\": \"openai-completions\",",
		"      \"apiKey\": " + jsonString(r.EnvVar) + ",",
		"      \"authHeader\": true,",
		"      \"models\": [",
		"        { \"id\": " + jsonString(r.Model) + " }",
		"      ]",
		"    }",
		"  }",
		"}",
	}, "\n")

	return Guide{
		ID:          "pi",
		Name:        "Pi",
		Description: fmt.Sprintf("Register %s as a custom OpenAI-compatible provider in Pi's model configuration.", r.BrandName),
		Files: []GuideFile{{
			Path:     "~/.pi/agent/models.json",
			Language: "json",
			Content:  content,
		}},
		Commands: []GuideBlock{{Language: "bash", Content: keyExport(r) + "\n\npi"}},
		Notes: []string{
			fmt.Sprintf("The apiKey field names an environment variable, not the credential itself, so %s stays out of the file.", r.EnvVar),
			"authHeader: true makes Pi send Authorization: Bearer, which is what the gateway expects.",
			"If you already have a models.json, merge the providers entry rather than replacing the file.",
		},
		AgentPrompt: agentPrompt(r, "Pi",
			"Add a custom provider to ~/.pi/agent/models.json using the openai-completions API with authHeader enabled, merging with any existing providers."),
	}
}

func opencode(r resolved) Guide {
	content := strings.Join([]string{
		"{",
		"  \"$schema\": \"https://opencode.ai/config.json\",",
		"  \"provider\": {",
		"    " + jsonString(r.ProviderID) + ": {",
		"      \"npm\": \"@ai-sdk/openai-compatible\",",
		"      \"name\": " + jsonString(r.BrandName) + ",",
		"      \"options\": {",
		"        \"baseURL\": " + jsonString(r.APIBase) + ",",
		"        \"apiKey\": " + jsonString("{env:"+r.EnvVar+"}") + "",
		"      },",
		"      \"models\": {",
		"        " + jsonString(r.Model) + ": {",
		"          \"name\": " + jsonString(r.Model),
		"        }",
		"      }",
		"    }",
		"  }",
		"}",
	}, "\n")

	return Guide{
		ID:          "opencode",
		Name:        "OpenCode",
		Description: fmt.Sprintf("Add %s as a custom OpenAI-compatible provider in OpenCode's config.", r.BrandName),
		Files: []GuideFile{{
			Path:     "~/.config/opencode/opencode.json",
			Language: "json",
			Content:  content,
		}},
		Commands: []GuideBlock{{Language: "bash", Content: keyExport(r) + "\n\nopencode"}},
		Notes: []string{
			fmt.Sprintf("The {env:%s} placeholder reads the credential from your environment at run time.", r.EnvVar),
			"OpenCode's provider schema has changed across releases. If this does not load, check the current docs at opencode.ai and report it so the snippet can be updated.",
			"A project-local opencode.json overrides the global one, which is useful for trying this on a single repository first.",
		},
		AgentPrompt: agentPrompt(r, "OpenCode",
			"Add a custom openai-compatible provider to my OpenCode config, reading the API key from the environment rather than hardcoding it."),
	}
}

func codex(r resolved) Guide {
	content := strings.Join([]string{
		"model = " + tomlString(r.Model),
		"model_provider = " + tomlString(r.ProviderID),
		"",
		"[model_providers." + r.ProviderID + "]",
		"name = " + tomlString(r.BrandName),
		"base_url = " + tomlString(r.APIBase),
		"env_key = " + tomlString(r.EnvVar),
		"wire_api = \"responses\"",
	}, "\n")

	return Guide{
		ID:          "codex",
		Name:        "Codex",
		Description: fmt.Sprintf("Configure %s as a custom model provider for Codex, over the OpenAI Responses API.", r.BrandName),
		Files: []GuideFile{{
			Path:     "~/.codex/config.toml",
			Language: "toml",
			Content:  content,
		}},
		Commands: []GuideBlock{{Language: "bash", Content: keyExport(r) + "\n\ncodex"}},
		Notes: []string{
			"env_key names the environment variable Codex reads the credential from.",
			"Custom Codex providers use the Responses wire format, which the endpoint serves at /v1/responses.",
			"If you already have a config.toml, merge these keys instead of overwriting it.",
		},
		AgentPrompt: agentPrompt(r, "Codex",
			"Add a [model_providers.*] section to ~/.codex/config.toml with wire_api = \"responses\" and env_key pointing at my environment variable, preserving my existing settings."),
	}
}

func crush(r resolved) Guide {
	content := strings.Join([]string{
		"{",
		"  \"$schema\": \"https://charm.land/crush.json\",",
		"  \"providers\": {",
		"    " + jsonString(r.ProviderID) + ": {",
		"      \"type\": \"openai-compat\",",
		"      \"base_url\": " + jsonString(r.APIBase) + ",",
		"      \"api_key\": " + jsonString("$"+r.EnvVar) + ",",
		"      \"models\": [",
		"        {",
		"          \"id\": " + jsonString(r.Model) + ",",
		"          \"name\": " + jsonString(r.Model),
		"        }",
		"      ]",
		"    }",
		"  }",
		"}",
	}, "\n")

	return Guide{
		ID:          "crush",
		Name:        "Crush",
		Description: fmt.Sprintf("Add %s as an OpenAI-compatible provider in Crush.", r.BrandName),
		Files: []GuideFile{{
			Path:     "~/.config/crush/crush.json",
			Language: "json",
			Content:  content,
		}},
		Commands: []GuideBlock{{Language: "bash", Content: keyExport(r) + "\n\ncrush"}},
		Notes: []string{
			fmt.Sprintf("The $%s form tells Crush to expand the value from your environment.", r.EnvVar),
			"The openai-compat provider type sends Authorization: Bearer, which is what the gateway expects.",
			"Crush also supports an anthropic provider type; the OpenAI-compatible route is the tested one here.",
		},
		AgentPrompt: agentPrompt(r, "Crush",
			"Add an openai-compat provider to my Crush config that expands the API key from an environment variable."),
	}
}

// agentPrompt builds the paste-into-another-agent instructions.
//
// It is written as a direct instruction with an explicit "do not commit the
// key" constraint, because the most likely way this credential leaks is an
// agent helpfully writing it into a tracked config file.
func agentPrompt(r resolved, client, specifics string) string {
	return strings.Join([]string{
		fmt.Sprintf("Configure %s on this machine to use my self-hosted %s endpoint at %s.", client, r.BrandName, r.BaseURL),
		specifics,
		fmt.Sprintf("Use the model %q.", r.Model),
		fmt.Sprintf("Read the API key from the %s environment variable. Do not write the key itself into any file in a git repository, and do not print it.", r.EnvVar),
		"Preserve my existing unrelated configuration.",
	}, " ")
}
