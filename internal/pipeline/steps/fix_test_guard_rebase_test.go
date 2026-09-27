package steps

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
)

func TestCIFixGuardAllowsRebaseThatPreservesBaselineTests(t *testing.T) {
	dir, baseSHA, _ := setupGitRepo(t)
	testPath := filepath.Join(dir, "trusted_test.go")
	originalTest := "package example\nfunc TestTrusted(t *testing.T) {}\n"
	if err := os.WriteFile(testPath, []byte(originalTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "add trusted test")
	baseline := gitCmd(t, dir, "rev-parse", "HEAD")
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "fixer"}, dir, baseSHA, baseline, config.Commands{})

	// Advance main, then perform the same rebase the CI fixer is instructed to
	// do when resolving merge conflicts. The original feature commits get new
	// SHAs even though the existing test remains byte-for-byte unchanged.
	gitCmd(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "main-update.txt"), []byte("main update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "main-update.txt")
	gitCmd(t, dir, "commit", "-m", "advance main")
	gitCmd(t, dir, "checkout", "feature")
	gitCmd(t, dir, "rebase", "main")
	candidate := gitCmd(t, dir, "rev-parse", "HEAD")
	if _, err := stepGitRun(sctx, "merge-base", "--is-ancestor", baseline, candidate); err == nil {
		t.Fatal("test setup did not rewrite feature history")
	}

	changed, err := (&CIStep{}).recordLocalRepair(sctx, candidate)
	if err != nil {
		t.Fatalf("CI repair rejected a rebase that preserved baseline tests: %v", err)
	}
	if !changed {
		t.Fatal("rebased CI repair was not adopted")
	}
	if got := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); got != candidate {
		t.Fatalf("feature ref = %s, want rebased candidate %s", got, candidate)
	}
	stored, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil || stored == nil || stored.HeadSHA != candidate {
		t.Fatalf("persisted run = %+v, %v; want rebased candidate %s", stored, err, candidate)
	}
	content, err := os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("baseline test after rebase = %q, %v", content, err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != candidate {
		t.Fatalf("worktree head = %s, want rebased candidate %s", got, candidate)
	}
}

func TestCIFixGuardRejectsRebaseThatChangesBaselineTests(t *testing.T) {
	dir, baseSHA, _ := setupGitRepo(t)
	testPath := filepath.Join(dir, "trusted_test.go")
	originalTest := "package example\nfunc TestTrusted(t *testing.T) {}\n"
	if err := os.WriteFile(testPath, []byte(originalTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "add trusted test")
	baseline := gitCmd(t, dir, "rev-parse", "HEAD")
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "fixer"}, dir, baseSHA, baseline, config.Commands{})

	gitCmd(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "main-update.txt"), []byte("main update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "main-update.txt")
	gitCmd(t, dir, "commit", "-m", "advance main")
	gitCmd(t, dir, "checkout", "feature")
	gitCmd(t, dir, "rebase", "main")
	if err := os.WriteFile(testPath, []byte("package example\nfunc TestTrusted(t *testing.T) { t.Fatal(\"weakened\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "change trusted test")
	candidate := gitCmd(t, dir, "rev-parse", "HEAD")

	changed, err := (&CIStep{}).recordLocalRepair(sctx, candidate)
	var blocked *protectedTestChange
	if changed || !errors.As(err, &blocked) {
		t.Fatalf("CI rebase that changed a baseline test accepted: changed=%t err=%v", changed, err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != baseline {
		t.Fatalf("worktree head after rejected rebase = %s, want baseline %s", got, baseline)
	}
	if got := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); got != baseline {
		t.Fatalf("feature ref after rejected rebase = %s, want baseline %s", got, baseline)
	}
	content, err := os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("baseline test after rejected rebase = %q, %v", content, err)
	}
}
