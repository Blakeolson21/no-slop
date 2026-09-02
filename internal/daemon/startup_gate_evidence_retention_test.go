package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

// TestMigrateGateConfigsBackfillsRetentionOnAlreadyCurrentGates covers the gate
// population that predates reviewed-tree evidence: it is already stamped
// current, so migration takes its cheap path and never rewrites its config.
// Startup must still install the retention policy there, or every gate created
// before this feature silently keeps expiring reviewed trees. What that policy
// does to an aggressive gc is owned by
// internal/evidence.TestGateGCProtectionIsNamespaceSpecific.
func TestMigrateGateConfigsBackfillsRetentionOnAlreadyCurrentGates(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	const id = "legacy-current"
	bareDir := p.RepoDir(id)
	if err := git.InitBare(ctx, bareDir); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertRepoWithID(id, t.TempDir(), "https://example.com/legacy.git", "main"); err != nil {
		t.Fatal(err)
	}

	// Bring the gate to "current" the way an older release left it, then strip
	// the retention keys so the gate looks exactly like a pre-feature install.
	if stats := migrateGateConfigs(ctx, database, p); stats.Migrated != 1 {
		t.Fatalf("setup migration stats = %+v, want one migrated gate", stats)
	}
	retentionKeys := []string{
		"gc.refs/gate-evidence/*.reflogExpire",
		"gc.refs/gate-evidence/*.reflogExpireUnreachable",
	}
	for _, key := range retentionKeys {
		bareGit(t, bareDir, "config", "--unset", key)
		if got := bareGitConfig(t, bareDir, key); got != "" {
			t.Fatalf("setup left %s = %q", key, got)
		}
	}
	if !git.GateConfigCurrent(bareDir) {
		t.Fatal("setup gate is not stamped current")
	}

	stats := migrateGateConfigs(ctx, database, p)
	if stats.Gates != 1 || stats.Current != 1 || stats.Migrated != 0 || stats.Failed != 0 {
		t.Fatalf("migration stats = %+v, want one current gate repaired in place", stats)
	}
	for _, key := range retentionKeys {
		if got := bareGitConfig(t, bareDir, key); got != "never" {
			t.Errorf("current gate %s = %q, want never", key, got)
		}
	}
}

func bareGit(t *testing.T, bareDir string, args ...string) {
	t.Helper()
	full := append([]string{"--git-dir=" + bareDir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func bareGitConfig(t *testing.T, bareDir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+bareDir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
