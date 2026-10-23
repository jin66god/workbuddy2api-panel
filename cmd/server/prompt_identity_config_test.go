package main

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/prompt"
)

func TestPromptIdentityOverrideConfig(t *testing.T) {
	legacy, err := ParseConfig([]byte(`{"prompt":{"mode":"passthrough"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Prompt.IdentityOverrideEnabled || legacy.Prompt.IdentityOverrideText != prompt.DefaultHarnessIdentity {
		t.Fatalf("legacy settings changed: %+v", legacy.Prompt)
	}

	enabled, err := ParseConfig([]byte(`{"prompt":{"mode":"append","identity_override_enabled":true,"identity_override_text":"You are My Agent."}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Prompt.IdentityOverrideEnabled || enabled.Prompt.IdentityOverrideText != "You are My Agent." {
		t.Fatalf("override settings lost: %+v", enabled.Prompt)
	}
	if enabled.PromptText == "" {
		t.Fatal("append prompt text should still load")
	}

	reset, err := ParseConfig([]byte(`{"prompt":{"mode":"custom","identity_override_enabled":true,"identity_override_text":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reset.Prompt.IdentityOverrideEnabled || reset.Prompt.IdentityOverrideText != prompt.DefaultHarnessIdentity {
		t.Fatalf("empty custom identity should restore default without erasing checkbox: %+v", reset.Prompt)
	}
}
