package gate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/paths"
)

// TestInitConfiguresReviewedTreeRetention pins the registration side of gate
// evidence: a gate created by init must already carry the reviewed-tree
// retention policy, so the first review round of the first run is retained
// without any later repair pass. What that policy actually does to an
// aggressive gc is owned by
// internal/evidence.TestGateGCProtectionIsNamespaceSpecific.
func TestInitConfiguresReviewedTreeRetention(t *testing.T) {
	workDir := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	d := openTestDB(t, p)

	repo, _, err := Init(context.Background(), d, p, workDir)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	bareDir := p.RepoDir(repo.ID)
	for _, key := range []string{
		"gc.refs/gate-evidence/*.reflogExpire",
		"gc.refs/gate-evidence/*.reflogExpireUnreachable",
	} {
		if got := gateConfig(t, bareDir, key); got != "never" {
			t.Errorf("gate %s = %q, want never", key, got)
		}
	}

	// The retention policy belongs to the managed mirror only; the developer's
	// own repository keeps its default expiry behaviour.
	if got := gateConfig(t, workDir, "gc.refs/gate-evidence/*.reflogExpire"); got != "" {
		t.Errorf("working repository gained gate retention config: %q", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git", "refs", "gate-evidence")); !os.IsNotExist(err) {
		t.Errorf("working repository gained a gate-evidence ref store: %v", err)
	}
}

// gateConfig reads one config key through git itself, returning "" when unset.
func gateConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+gitDirOf(t, dir), "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitDirOf(t *testing.T, dir string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return filepath.Join(dir, ".git")
	}
	return dir
}
