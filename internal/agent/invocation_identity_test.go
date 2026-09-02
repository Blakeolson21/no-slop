package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestInvocationIdentityResolvesSymlinkAndKeepsOnlyModelSelectors(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-reviewer")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "claude-wrapper")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	ag, err := New(types.AgentClaude, link, []string{
		"--model", "z-ai/glm-5.3-flash",
		"--api-key", "must-not-be-recorded",
		"--model-provider=openrouter",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := ResolveInvocationIdentity(ag)
	if identity.Executable == nil || *identity.Executable != target {
		t.Fatalf("resolved executable = %v, want %q", identity.Executable, target)
	}
	wantArgs := []string{"--model", "z-ai/glm-5.3-flash", "--model-provider=openrouter"}
	if !reflect.DeepEqual(identity.ModelArgs, wantArgs) {
		t.Fatalf("model args = %#v, want %#v", identity.ModelArgs, wantArgs)
	}
}

func TestInvocationIdentityMissingExecutableIsUnknown(t *testing.T) {
	ag, err := New(types.AgentClaude, filepath.Join(t.TempDir(), "missing"), nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := ResolveInvocationIdentity(ag)
	if identity.Executable != nil {
		t.Fatalf("missing executable must be unknown, got %q", *identity.Executable)
	}
	if identity.ModelArgs == nil || len(identity.ModelArgs) != 0 {
		t.Fatalf("inspected argv without a model selector should be known empty, got %#v", identity.ModelArgs)
	}
}
