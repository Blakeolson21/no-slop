//go:build linux

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestPhantomWorkerOwnersResolvesSymlinkedStateRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	p := paths.WithRoot(linkedRoot)
	worktree := p.WorktreeDir("repo", "run")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.Dir = worktree
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	owners, err := phantomWorkerOwners(p, []db.PhantomRun{{ID: "run", RepoID: "repo", CreatedAt: time.Now().Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	if owners["run"] == "" {
		t.Fatalf("worker ownership not detected: %v", owners)
	}
}
