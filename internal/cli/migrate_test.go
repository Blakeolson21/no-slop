package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestMigrateDryRunOpensOnlyExistingRegistry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_HOME", root)
	t.Setenv("NM_HOME", root)
	p := paths.WithRoot(root)
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeCmd("migrate", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v; output %s", err, out)
	}
	var report struct {
		DryRun   bool `json:"dry_run"`
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || !report.Verified {
		t.Fatalf("unexpected report %s", out)
	}
	after, err := os.ReadFile(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run changed registry bytes")
	}
	if _, err := os.Stat(filepath.Join(root, "stores")); !os.IsNotExist(err) {
		t.Fatal("dry-run created stores")
	}
	if _, err := executeCmd("migrate"); err == nil {
		t.Fatal("migration without --dry-run must refuse")
	}
	missing := filepath.Join(root, "missing")
	t.Setenv("NS_HOME", missing)
	t.Setenv("NM_HOME", missing)
	if _, err := executeCmd("migrate", "--dry-run"); err == nil {
		t.Fatal("missing registry should fail")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("dry-run created state root")
	}
}

func TestMigrateDryRunPrintsUnverifiedReportAndFailsForMissingLiveGate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_HOME", root)
	t.Setenv("NM_HOME", root)
	p := paths.WithRoot(root)
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepoWithID("missing", filepath.Join(root, "checkout"), "https://example.test/project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertRun(repo.ID, "feature", strings.Repeat("a", 40), strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := executeCmd("migrate", "--dry-run")
	if err == nil {
		t.Fatal("missing live gate must fail verification")
	}
	var report struct {
		Verified       bool `json:"verified"`
		RemovalAllowed bool `json:"removal_allowed"`
		Sources        []struct {
			LiveRuns int      `json:"live_runs"`
			Issues   []string `json:"issues"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Verified || report.RemovalAllowed || len(report.Sources) != 1 || report.Sources[0].LiveRuns != 1 || len(report.Sources[0].Issues) == 0 {
		t.Fatalf("invalid unverified report %s", out)
	}
	if _, err := os.Stat(p.RepoDir(repo.ID)); !os.IsNotExist(err) {
		t.Fatal("planner created missing gate")
	}
}
