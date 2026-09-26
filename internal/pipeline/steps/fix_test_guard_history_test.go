package steps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
)

func TestCIFixGuardRejectsRewindThatDropsBaselineTest(t *testing.T) {
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

	// Model a CI fixer resetting HEAD to a commit before the recorded suite.
	gitCmd(t, dir, "checkout", "--detach", baseSHA)
	changed, err := (&CIStep{}).recordLocalRepair(sctx, baseSHA)
	if err == nil {
		t.Fatalf("CI repair accepted ancestor %s and dropped baseline test (changed=%t)", baseSHA, changed)
	}
	if changed {
		t.Fatal("rejected rewind was reported as an adopted repair")
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != baseline {
		t.Fatalf("worktree head after rejection = %s, want restored baseline %s", got, baseline)
	}
	if got := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); got != baseline {
		t.Fatalf("feature ref after rejection = %s, want baseline %s", got, baseline)
	}
	stored, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil || stored == nil || stored.HeadSHA != baseline {
		t.Fatalf("persisted run after rejection = %+v, %v; want baseline %s", stored, err, baseline)
	}
	content, err := os.ReadFile(testPath)
	if err != nil || string(content) != originalTest {
		t.Fatalf("baseline test after rejection = %q, %v", content, err)
	}
}
