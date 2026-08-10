package keystore

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "trims surrounding space", input: "  MacBook  ", want: "MacBook"},
		{name: "keeps inner space", input: "MacBook Claude Code", want: "MacBook Claude Code"},
		{name: "allows unicode", input: "Работа ноутбук", want: "Работа ноутбук"},
		{name: "rejects empty", input: "", wantErr: true},
		{name: "rejects whitespace only", input: "   \t ", wantErr: true},
		{name: "rejects newline", input: "line\nbreak", wantErr: true},
		{name: "rejects tab", input: "tab\there", wantErr: true},
		{name: "rejects null byte", input: "null\x00byte", wantErr: true},
		// A CR would let a name forge a second line in a log record.
		{name: "rejects carriage return", input: "carriage\rreturn", wantErr: true},
		{name: "allows exactly the limit", input: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "rejects over the limit", input: strings.Repeat("a", 65), wantErr: true},
		// Length is counted in runes, so a multi-byte name is not penalised.
		{name: "counts runes not bytes", input: strings.Repeat("é", 64), want: strings.Repeat("é", 64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateName(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateName(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ValidateName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGenerateFormat(t *testing.T) {
	got, err := Generate("llm_")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if !strings.HasPrefix(got.Secret, "llm_") {
		t.Errorf("Secret = %q, want the configured prefix", got.Secret)
	}

	encoded := strings.TrimPrefix(got.Secret, "llm_")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("secret body is not base64url: %v", err)
	}
	// The design brief sets a floor of 256 bits of random material.
	if len(raw) < 32 {
		t.Errorf("secret carries %d bytes of entropy, want at least 32", len(raw))
	}

	if got.Suffix != encoded[len(encoded)-len(got.Suffix):] {
		t.Errorf("Suffix %q is not the tail of the credential", got.Suffix)
	}
	if len(got.Suffix) != suffixLen {
		t.Errorf("Suffix length = %d, want %d", len(got.Suffix), suffixLen)
	}

	// The resource ID becomes part of a Kubernetes object name, which must be
	// a lowercase RFC 1123 label.
	if got.ID == "" || strings.ToLower(got.ID) != got.ID {
		t.Errorf("ID = %q, want a non-empty lowercase identifier", got.ID)
	}
	for _, r := range got.ID {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			t.Errorf("ID = %q contains %q, which is not valid in a resource name", got.ID, r)
			break
		}
	}

	if !strings.HasPrefix(got.ClientID, "client-") {
		t.Errorf("ClientID = %q, want a client- prefix", got.ClientID)
	}
	// The client identifier may appear in gateway logs, so it must not be
	// derived from the credential.
	if strings.Contains(got.ClientID, encoded) || strings.Contains(encoded, got.ClientID) {
		t.Error("ClientID and the credential share material; they must be independent")
	}
}

func TestGenerateIsUnique(t *testing.T) {
	const runs = 500
	secrets := make(map[string]bool, runs)
	ids := make(map[string]bool, runs)

	for i := 0; i < runs; i++ {
		got, err := Generate("llm_")
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if secrets[got.Secret] {
			t.Fatal("Generate produced a duplicate credential")
		}
		if ids[got.ID] {
			t.Fatal("Generate produced a duplicate resource ID")
		}
		secrets[got.Secret], ids[got.ID] = true, true
	}
}

func TestOwnerIDValid(t *testing.T) {
	// A partially empty identity must never be treated as usable: it would
	// match broadly in a label selector.
	tests := []struct {
		name  string
		owner OwnerID
		want  bool
	}{
		{"both present", OwnerID{TenantID: "t", ObjectID: "o"}, true},
		{"missing object", OwnerID{TenantID: "t"}, false},
		{"missing tenant", OwnerID{ObjectID: "o"}, false},
		{"empty", OwnerID{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.owner.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
