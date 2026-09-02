package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// identityTempDir returns a temp dir with symlinks already resolved, so a path
// built from it is directly comparable with one the resolver ran through
// filepath.EvalSymlinks. On macOS t.TempDir() lives under /var, which is itself
// a symlink to /private/var, and on Windows it can be an 8.3 short path.
func identityTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// identityExecutableName gives a filename exec.LookPath can resolve on this
// platform. Windows finds only names carrying a PATHEXT extension, so an
// extension-less unix-style wrapper name is not an executable there.
func identityExecutableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestInvocationIdentityResolvesSymlinkAndKeepsOnlyModelSelectors(t *testing.T) {
	dir := identityTempDir(t)
	target := filepath.Join(dir, identityExecutableName("real-reviewer"))
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, identityExecutableName("claude-wrapper"))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
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

// TestInvocationIdentityACPWrapperLeavesUninspectedArgvUnknown pins the two
// halves of the nil-vs-known-empty convention for the ACP wrapper, whose model
// lives inside an opaque rawCommand that is never inspected: ModelArgs must stay
// nil (unknown) rather than the known-empty slice that would claim argv was
// examined, and configured extra args must not be recorded as if they had been.
func TestInvocationIdentityACPWrapperLeavesUninspectedArgvUnknown(t *testing.T) {
	dir := identityTempDir(t)
	bin := filepath.Join(dir, identityExecutableName("acpx"))
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ag, err := New(types.AgentCursor, bin, []string{"--model", "never-inspected"})
	if err != nil {
		t.Fatal(err)
	}
	identity := ResolveInvocationIdentity(ag)
	if identity.ModelArgs != nil {
		t.Fatalf("uninspected argv must stay unknown (nil), got %#v", identity.ModelArgs)
	}
	if identity.ConfiguredAgent != string(types.AgentCursor) {
		t.Fatalf("configured agent = %q, want %q", identity.ConfiguredAgent, types.AgentCursor)
	}
	if identity.Executable == nil || *identity.Executable != bin {
		t.Fatalf("resolved executable = %v, want %q", identity.Executable, bin)
	}
}
