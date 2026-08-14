package pipeline

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
)

// TestMain unsets ambient GIT_CONFIG_* injection from agent harnesses so the
// real-git tests in this package see hermetic git behavior (issue #362).
func TestMain(m *testing.M) {
	os.Unsetenv("GIT_CONFIG_COUNT")
	os.Exit(m.Run())
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The submitted-diff file list is base..submitted-head, not base..HEAD: a fix
// round's commits must not launder fixer-created files into the "submitted"
// surface.
func TestSubmittedDiffFiles(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	writeFile(t, dir+"/a.txt", "base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "base")
	baseSHA := gitRun(t, dir, "rev-parse", "HEAD")

	writeFile(t, dir+"/a.txt", "changed\n")
	writeFile(t, dir+"/b.txt", "new\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "submitted change")
	submittedSHA := gitRun(t, dir, "rev-parse", "HEAD")

	// A later fix-round commit creates a brand-new file; it is NOT part of
	// the submitted diff.
	writeFile(t, dir+"/fixer-created.txt", "fix\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "fix round")
	fixSHA := gitRun(t, dir, "rev-parse", "HEAD")

	run := &db.Run{BaseSHA: baseSHA, HeadSHA: fixSHA, SubmittedHeadSHA: &submittedSHA}
	files, ok := submittedDiffFiles(context.Background(), dir, run)
	if !ok {
		t.Fatal("submittedDiffFiles reported unknown for a resolvable diff")
	}
	got := strings.Join(files, ",")
	if got != "a.txt,b.txt" {
		t.Fatalf("submitted files = %q, want a.txt,b.txt", got)
	}

	// A run whose SHAs cannot be resolved reports unknown instead of an
	// empty (fabricated) list.
	bad := &db.Run{BaseSHA: "0000000000000000000000000000000000000000", HeadSHA: fixSHA, SubmittedHeadSHA: &submittedSHA}
	bad.BaseSHA = "does-not-exist"
	if _, ok := submittedDiffFiles(context.Background(), dir, bad); ok {
		t.Fatal("unresolvable base should report unknown")
	}
	if _, ok := submittedDiffFiles(context.Background(), dir, nil); ok {
		t.Fatal("nil run should report unknown")
	}
}
