package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	slopcli "github.com/kunchenguid/no-mistakes/internal/slop/cli"
)

func TestRunGatePrintsMarkdownTierAndReasons(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"blast radius:",
		"novelty:",
		"reversibility:",
		"review: skipped",
		"tests: skipped",
		"verdict: pass",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGatePrintsOverrideAndStillBlocksLeak(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "policy.go", "package policy\n")
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/policy")
	writeFile(t, dir, "policy.go", "package policy\n\nconst token = \"ghp_"+"abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\"\n")
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "change")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"override: single-review -> leak-scan-only",
		"finding: [leak-identity-scan] policy.go:3",
		"verdict: fail",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGateFailsClosedWhenExplicitBlocklistIsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--blocklist", "missing-private-names"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want evaluation failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "read private-name blocklist") {
		t.Fatalf("stderr does not name blocklist failure: %s", stderr.String())
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
