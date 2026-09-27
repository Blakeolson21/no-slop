package steps

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
)

func TestCIFixGuardRejectsRebasedMergedTestEditAndRevert(t *testing.T) {
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

	// Rebase the recorded feature branch onto an advanced main, then merge a
	// side branch whose existing-test edit and revert leave the final tree
	// unchanged. A first-parent-only walk misses both side-branch commits.
	gitCmd(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "main-update.txt"), []byte("main update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "main-update.txt")
	gitCmd(t, dir, "commit", "-m", "advance main")
	gitCmd(t, dir, "checkout", "feature")
	gitCmd(t, dir, "rebase", "main")
	gitCmd(t, dir, "checkout", "-b", "side-test-history")
	if err := os.WriteFile(testPath, []byte("package example\nfunc TestTrusted(t *testing.T) { t.Fatal(\"weakened\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "weaken existing test on side branch")
	if err := os.WriteFile(testPath, []byte(originalTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "restore existing test on side branch")

	gitCmd(t, dir, "checkout", "feature")
	if err := os.WriteFile(filepath.Join(dir, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "repair.txt")
	gitCmd(t, dir, "commit", "-m", "repair product code")
	gitCmd(t, dir, "merge", "--no-ff", "side-test-history", "-m", "merge side branch")
	candidate := gitCmd(t, dir, "rev-parse", "HEAD")

	content, err := os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("candidate test = %q, %v; want baseline content", content, err)
	}
	if got := gitCmd(t, dir, "diff", baseline, candidate, "--", "trusted_test.go"); got != "" {
		t.Fatalf("test setup changed final tree for trusted_test.go: %s", got)
	}

	changed, err := (&CIStep{}).recordLocalRepair(sctx, candidate)
	var blocked *protectedTestChange
	if changed || !errors.As(err, &blocked) {
		t.Fatalf("CI repair accepted merged test edit-and-revert history: changed=%t err=%v", changed, err)
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

func TestFixTestHistoryChangesIncludesMergeParentTestDiff(t *testing.T) {
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

	gitCmd(t, dir, "checkout", "-b", "side-test-history")
	if err := os.WriteFile(testPath, []byte("package example\nfunc TestTrusted(t *testing.T) { t.Fatal(\"changed\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "trusted_test.go")
	gitCmd(t, dir, "commit", "-m", "change existing test on side branch")
	sideHead := gitCmd(t, dir, "rev-parse", "HEAD")

	gitCmd(t, dir, "checkout", "feature")
	gitCmd(t, dir, "merge", "-s", "ours", "--no-ff", "side-test-history", "-m", "keep first-parent test tree")
	mergeHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if got := gitCmd(t, dir, "diff", baseline, mergeHead, "--", "trusted_test.go"); got != "" {
		t.Fatalf("test setup changed first-parent tree for trusted_test.go: %s", got)
	}

	changes, err := fixTestHistoryChanges(sctx, sideHead, mergeHead, map[string]bool{"trusted_test.go": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].commit != mergeHead || len(changes[0].paths) != 1 || changes[0].paths[0] != "trusted_test.go" {
		t.Fatalf("merge-parent test change = %+v; want the merge commit and trusted_test.go", changes)
	}
}
