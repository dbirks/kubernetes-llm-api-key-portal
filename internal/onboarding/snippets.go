package onboarding

import (
	"fmt"
	"strconv"
	"strings"
)

// The snippets below were written against upstream documentation as of August
// 2026. Client configuration formats move faster than this portal does, so each
// one is covered by a golden test: when a format changes, update the builder
// here and run `go test ./internal/onboarding -update` to review the diff.

// Shell identifiers used to tag GuideBlocks so the account page's sh/PowerShell
// toggle can show one shell at a time. A block with an empty Shell (a config
// file's contents, Cursor's GUI values) is shell-agnostic and always shows.
const (
	shellSh = "sh"
	shellPS = "powershell"
	labelSh = "sh"
	labelPS = "PowerShell"
)

// exportLine renders a POSIX shell export, quoting the value.
func exportLine(name, value string) string {
	return fmt.Sprintf("export %s=%q", name, value)
}

// psSetLine renders a PowerShell environment assignment. PowerShell only
// interpolates inside double quotes, and none of the values here (URLs, model
// names, keys, derived identifiers) contain a `$` or a `"`, so a double-quoted
// literal is safe and reads the same as the sh form.
func psSetLine(name, value string) string {
	return fmt.Sprintf("$env:%s = %q", name, value)
}

// keyExport is the sh line that puts the credential in the environment. Every
// guide leads with it, so the credential lives in exactly one place per client
// rather than being pasted into a config file that might get committed.
func keyExport(r resolved) string { return exportLine(r.EnvVar, r.Key) }

// keyExportPS is the PowerShell equivalent of keyExport.
func keyExportPS(r resolved) string { return psSetLine(r.EnvVar, r.Key) }

// shBlock and psBlock tag a snippet with its shell so the toggle can pick it.
func shBlock(content string) GuideBlock {
	return GuideBlock{Language: labelSh, Shell: shellSh, Content: content}
}
func psBlock(content string) GuideBlock {
	return GuideBlock{Language: labelPS, Shell: shellPS, Content: content}
}

// contextWindow is the shared context length advertised in config files that
// want one. All six models expose a large window; the exact figure is a hint
// the client uses for its own budgeting, not a hard limit the gateway enforces.
const contextWindow = 262144

// maxTokensFor is the default completion budget a config file suggests. A
// reasoning model spends tokens thinking before it answers, so it gets more
// headroom or the visible reply can be cut off mid-thought.
func maxTokensFor(r resolved) int {
	if r.Kind == KindReasoning {
		return 16384
	}
	return 8192
}

// launchBlocks builds the sh + PowerShell command pair a config-file client
// needs: export the credential, then run the tool. The file itself is
// shell-agnostic and rendered separately; only the credential export differs
// between shells.
func launchBlocks(r resolved, launch string) []GuideBlock {
	return []GuideBlock{
		shBlock(keyExport(r) + "\n\n" + launch),
		psBlock(keyExportPS(r) + "\n\n" + launch),
	}
}

func claudeCode(r resolved) Guide {
	sh := strings.Join([]string{
		keyExport(r),
		"",
		exportLine("ANTHROPIC_BASE_URL", r.AnthropicBase),
		fmt.Sprintf("export ANTHROPIC_AUTH_TOKEN=\"$%s\"", r.EnvVar),
		exportLine("ANTHROPIC_DEFAULT_OPUS_MODEL", r.Model),
		exportLine("ANTHROPIC_DEFAULT_SONNET_MODEL", r.Model),
		exportLine("ANTHROPIC_DEFAULT_HAIKU_MODEL", r.Model),
		"",
		"claude",
	}, "\n")
	ps := strings.Join([]string{
		keyExportPS(r),
		"",
		psSetLine("ANTHROPIC_BASE_URL", r.AnthropicBase),
		fmt.Sprintf("$env:ANTHROPIC_AUTH_TOKEN = $env:%s", r.EnvVar),
		psSetLine("ANTHROPIC_DEFAULT_OPUS_MODEL", r.Model),
		psSetLine("ANTHROPIC_DEFAULT_SONNET_MODEL", r.Model),
		psSetLine("ANTHROPIC_DEFAULT_HAIKU_MODEL", r.Model),
		"",
		"claude",
	}, "\n")

	return Guide{
		ID:          "claude-code",
		Name:        "Claude Code",
		Description: fmt.Sprintf("Point Claude Code at %s's Anthropic-compatible surface at /anthropic. Claude Code appends /v1/messages itself, and the gateway translates between the Anthropic and OpenAI APIs, so every served model is reachable on this one URL.", r.BrandName),
		Commands:    []GuideBlock{shBlock(sh), psBlock(ps)},
		Notes: []string{
			"Add the exports to your shell profile to make them permanent.",
			"The three model variables map Claude Code's Opus, Sonnet, and Haiku slots onto the single served model, so every mode routes to the same place.",
			"ANTHROPIC_BASE_URL ends in /anthropic (not /v1): that path is what tells the gateway to translate Anthropic requests. Claude Code appends /v1/messages on its own.",
			"ANTHROPIC_AUTH_TOKEN is the gateway bearer token. Leave ANTHROPIC_API_KEY unset so it cannot take precedence.",
		},
		AgentPrompt: agentPromptFor(r, r.AnthropicBase, "Claude Code",
			fmt.Sprintf("Set ANTHROPIC_BASE_URL to %s, ANTHROPIC_AUTH_TOKEN, and the ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL variables in my shell profile.", r.AnthropicBase)),
	}
}

func openaiCompatible(r resolved) Guide {
	sh := strings.Join([]string{
		keyExport(r),
		"",
		exportLine("OPENAI_BASE_URL", r.APIBase),
		fmt.Sprintf("export OPENAI_API_KEY=\"$%s\"", r.EnvVar),
	}, "\n")
	ps := strings.Join([]string{
		keyExportPS(r),
		"",
		psSetLine("OPENAI_BASE_URL", r.APIBase),
		fmt.Sprintf("$env:OPENAI_API_KEY = $env:%s", r.EnvVar),
	}, "\n")

	return Guide{
		ID:          "openai",
		Name:        "OpenAI-compatible",
		Description: fmt.Sprintf("Point any OpenAI-compatible client or SDK at %s. These are the two variables every OpenAI SDK already reads: the official Python and Node clients, LiteLLM, LangChain, LlamaIndex, and most tools that take a custom base URL.", r.BrandName),
		Commands:    []GuideBlock{shBlock(sh), psBlock(ps)},
		Notes: []string{
			fmt.Sprintf("Set the request's \"model\" field to %q. The same key reaches every model; only the model name changes.", r.Model),
			"The endpoint speaks the OpenAI Chat Completions API at /v1/chat/completions, which is what these clients use.",
			fmt.Sprintf("OPENAI_API_KEY reads from $%s so the credential stays in your environment rather than a file.", r.EnvVar),
		},
		AgentPrompt: agentPrompt(r, "an OpenAI-compatible client",
			fmt.Sprintf("Set OPENAI_BASE_URL to %s and OPENAI_API_KEY to the credential from my environment.", r.APIBase)),
	}
}

func cursor(r resolved) Guide {
	// Cursor is configured in its Settings UI, not a file on disk, so the
	// "file" here is the set of exact values to paste into those fields. The
	// credential lives in the element body only, like every other snippet.
	values := strings.Join([]string{
		"Base URL:  " + r.APIBase,
		"API key:   " + r.Key,
		"Model:     " + r.Model,
	}, "\n")

	return Guide{
		ID:          "cursor",
		Name:        "Cursor",
		Description: fmt.Sprintf("Cursor talks to any OpenAI-compatible endpoint. Add %s in Settings → Models, then add the model by name so you can select it.", r.BrandName),
		Files: []GuideFile{{
			Path:     "Cursor · Settings → Models → OpenAI API Key",
			Language: "text",
			Content:  values,
		}},
		Notes: []string{
			"Open Settings → Models (Cmd/Ctrl+Shift+J, then Models).",
			"Enable “Override OpenAI Base URL” and paste the Base URL above.",
			"Paste the API key into the OpenAI API Key field and click Verify.",
			fmt.Sprintf("Under “Model Names”, add %q so it shows up in the model picker.", r.Model),
			"Cursor holds one custom base URL at a time, so it points at one model at a time. To use a different model here, swap the base URL to that model's and change the model name.",
			"The custom base URL only powers Cursor's chat and agent panels; Tab completion and inline edits still go to Cursor's own backend.",
		},
		AgentPrompt: agentPrompt(r, "Cursor",
			"Configure Cursor's OpenAI API settings: override the base URL, set the API key, and add the model name as a custom model."),
	}
}

func curlGuide(r resolved) Guide {
	maxTokens := 256
	if r.Kind == KindReasoning {
		// A reasoning model spends tokens thinking before it replies, so a raw
		// request needs headroom or the visible answer is truncated.
		maxTokens = 4096
	}

	// A single-quoted body spans multiple lines in the shell, so the JSON reads
	// naturally without escaping.
	shLines := []string{
		keyExport(r),
		"",
		"curl " + r.APIBase + "/chat/completions \\",
		fmt.Sprintf("  -H \"Authorization: Bearer $%s\" \\", r.EnvVar),
		"  -H \"Content-Type: application/json\" \\",
		"  -d '{",
		"        \"model\": " + jsonString(r.Model) + ",",
		fmt.Sprintf("        \"max_tokens\": %d,", maxTokens),
		"        \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in one sentence.\"}]",
		"      }'",
	}
	sh := strings.Join(shLines, "\n")

	// PowerShell: curl.exe (not the Invoke-WebRequest alias), backtick line
	// continuations, and a single-quoted JSON body so nothing is interpolated.
	// The Authorization header is double-quoted so $env: expands.
	psLines := []string{
		keyExportPS(r),
		"",
		"curl.exe " + r.APIBase + "/chat/completions `",
		fmt.Sprintf("  -H \"Authorization: Bearer $env:%s\" `", r.EnvVar),
		"  -H \"Content-Type: application/json\" `",
		"  -d '{\"model\": " + jsonString(r.Model) + ", \"max_tokens\": " + strconv.Itoa(maxTokens) + ", \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in one sentence.\"}]}'",
	}
	ps := strings.Join(psLines, "\n")

	return Guide{
		ID:          "curl",
		Name:        "curl",
		Description: fmt.Sprintf("A one-shot request to confirm your key and %s are reachable, with no client to install.", r.BrandName),
		Commands:    []GuideBlock{shBlock(sh), psBlock(ps)},
		Notes: []string{
			"A 200 with a chat completion means the key, gateway, and model are all working.",
			fmt.Sprintf("Swap %q for any name from the model list to try a different model.", r.Model),
		},
		AgentPrompt: agentPrompt(r, "curl",
			fmt.Sprintf("Send a POST to %s/chat/completions with the model %q and the API key from my environment as a Bearer token.", r.APIBase, r.Model)),
	}
}

func pi(r resolved) Guide {
	// earendil-works/pi (@earendil-works/pi-coding-agent), configured at
	// ~/.pi/agent/models.json. api "openai-completions" is the Chat Completions
	// wire format the gateway serves; apiKey "$VAR" expands from the environment
	// at run time so the credential stays out of the file. Built with the JSON
	// encoder so an operator brand name containing quotes cannot break the file.
	content := strings.Join([]string{
		"{",
		"  \"providers\": {",
		"    " + jsonString(r.ProviderID) + ": {",
		"      \"baseUrl\": " + jsonString(r.APIBase) + ",",
		"      \"api\": \"openai-completions\",",
		"      \"apiKey\": " + jsonString("$"+r.EnvVar) + ",",
		"      \"models\": [",
		"        {",
		"          \"id\": " + jsonString(r.Model) + ",",
		"          \"name\": " + jsonString(r.Label) + ",",
		"          \"reasoning\": " + strconv.FormatBool(r.Kind == KindReasoning) + ",",
		"          \"contextWindow\": " + strconv.Itoa(contextWindow) + ",",
		"          \"maxTokens\": " + strconv.Itoa(maxTokensFor(r)),
		"        }",
		"      ]",
		"    }",
		"  }",
		"}",
	}, "\n")

	return Guide{
		ID:          "pi",
		Name:        "Pi",
		Description: fmt.Sprintf("Register %s as a custom OpenAI-compatible provider in Pi's model configuration (earendil-works/pi, ~/.pi/agent/models.json).", r.BrandName),
		Files: []GuideFile{{
			Path:     "~/.pi/agent/models.json",
			Language: "json",
			Content:  content,
		}},
		Commands: launchBlocks(r, "pi"),
		Notes: []string{
			fmt.Sprintf("The $%s form tells Pi to expand the credential from your environment, so the key never lands in the file.", r.EnvVar),
			"The openai-completions API sends Authorization: Bearer, which is what the gateway expects.",
			"If you already have a models.json, merge the providers entry rather than replacing the file.",
		},
		AgentPrompt: agentPrompt(r, "Pi",
			"Add a custom provider to ~/.pi/agent/models.json using the openai-completions API, expanding the API key from an environment variable, merging with any existing providers."),
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
		"          \"name\": " + jsonString(r.Label),
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
		Commands: launchBlocks(r, "opencode"),
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
		Commands: launchBlocks(r, "codex"),
		Notes: []string{
			"env_key names the environment variable Codex reads the credential from.",
			"Recent Codex (0.122+) only supports wire_api = \"responses\"; the older \"chat\" value fails at startup. This provider therefore targets the endpoint's OpenAI Responses API at /v1/responses.",
			"If Codex errors on startup with a 404 or an unsupported-endpoint message, this model's server build does not expose /v1/responses. Point base_url at a small Responses-to-Chat proxy (e.g. LiteLLM) instead; the other clients here use Chat Completions directly and need no proxy.",
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
		"          \"name\": " + jsonString(r.Label) + ",",
		"          \"context_window\": " + strconv.Itoa(contextWindow) + ",",
		"          \"default_max_tokens\": " + strconv.Itoa(maxTokensFor(r)),
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
		Commands: launchBlocks(r, "crush"),
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
	return agentPromptFor(r, r.BaseURL, client, specifics)
}

// agentPromptFor is agentPrompt with an explicit endpoint, for a client whose
// endpoint is not the model's OpenAI-style base URL. Claude Code uses it to
// name the shared Anthropic surface (origin + /anthropic) rather than the
// per-model base path.
func agentPromptFor(r resolved, endpoint, client, specifics string) string {
	return strings.Join([]string{
		fmt.Sprintf("Configure %s on this machine to use my self-hosted %s endpoint at %s.", client, r.BrandName, endpoint),
		specifics,
		fmt.Sprintf("Use the model %q.", r.Model),
		fmt.Sprintf("Read the API key from the %s environment variable. Do not write the key itself into any file in a git repository, and do not print it.", r.EnvVar),
		"Preserve my existing unrelated configuration.",
	}, " ")
}
