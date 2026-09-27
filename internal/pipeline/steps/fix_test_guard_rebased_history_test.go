package steps

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
)

func TestCIFixGuardRejectsRebasedTestEditThenRevert(t *testing.T) {
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

	// Rebase the feature commit, then add two commits that change and restore an
	// existing test. The final tree matches the baseline, but the repair history
	// still contains forbidden test edits.
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
	gitCmd(t, dir, "commit", "-m", "weaken existing test")
	if err := os.WriteFile(testPath, []byte(originalTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "restore existing test")
	candidate := gitCmd(t, dir, "rev-parse", "HEAD")
	content, err := os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("candidate test = %q, %v; want baseline content", content, err)
	}
	if got := gitCmd(t, dir, "diff", baseline, candidate, "--", "trusted_test.go"); got != "" {
		t.Fatalf("test setup changed the final tree for trusted_test.go: %s", got)
	}

	changed, err := (&CIStep{}).recordLocalRepair(sctx, candidate)
	var blocked *protectedTestChange
	if changed || !errors.As(err, &blocked) {
		t.Fatalf("CI repair accepted rebased test edit-and-revert history: changed=%t err=%v", changed, err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != baseline {
		t.Fatalf("worktree head after rejection = %s, want baseline %s", got, baseline)
	}
	if got := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); got != baseline {
		t.Fatalf("feature ref after rejection = %s, want baseline %s", got, baseline)
	}
	content, err = os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("baseline test after rejection = %q, %v", content, err)
	}
}
