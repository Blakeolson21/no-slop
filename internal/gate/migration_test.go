package gate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func migrationSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			result[rel] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = fmt.Sprintf("%x %v %v", sha256.Sum256(data), info.Mode(), info.ModTime())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func legacyMigrationGate(t *testing.T, p *paths.Paths, d *db.DB, work, id string) *db.Repo {
	t.Helper()
	ctx := context.Background()
	upstream, err := git.GetRemoteURL(ctx, work, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionGate(ctx, p.RepoDir(id), work, upstream, p.ReposDir(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, p.RepoDir(id), "-c", "fetch.unpackLimit=999999", "fetch", "--no-tags", work, "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepoWithID(id, work, upstream, "main")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestMigrationPlansLegacyStoresByUpstreamAndPreservesLiveRunRefs(t *testing.T) {
	ctx := context.Background()
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	base, err := git.HeadSHA(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	var runIDs []string
	for _, id := range []string{"legacy-one", "legacy-two"} {
		if _, err := git.Run(ctx, work, "commit", "--allow-empty", "-m", id); err != nil {
			t.Fatal(err)
		}
		head, err := git.HeadSHA(ctx, work)
		if err != nil {
			t.Fatal(err)
		}
		clone := filepath.Join(t.TempDir(), id)
		if _, err := git.Run(ctx, work, "clone", work, clone); err != nil {
			t.Fatal(err)
		}
		upstream, err := git.GetRemoteURL(ctx, work, "origin")
		if err != nil {
			t.Fatal(err)
		}
		if err := git.EnsureRemote(ctx, clone, "origin", upstream); err != nil {
			t.Fatal(err)
		}
		repo := legacyMigrationGate(t, p, d, clone, id)
		// The same branch name in both stores has different generations.
		if _, err := git.RunBare(ctx, p.RepoDir(id), "update-ref", "refs/heads/feature", head); err != nil {
			t.Fatal(err)
		}
		run, err := d.InsertRun(repo.ID, "refs/heads/feature", head, base)
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, run.ID)
	}
	// Freeze the fixture registry in rollback-journal mode. A WAL reader
	// necessarily updates SQLite's transient shared-memory read marks; this
	// fixture permits byte-for-byte assertions across every state file.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = db.OpenReadOnly(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	before := migrationSnapshot(t, p.Root())
	plan, err := PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || !plan.Verified || plan.RemovalAllowed || len(plan.Sources) != 2 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.Sources[0].Destination != plan.Sources[1].Destination {
		t.Fatal("same upstream planned into separate stores")
	}
	for i, source := range plan.Sources {
		if !source.Verified || source.LiveRuns != 1 || source.Action != "merge-objects" {
			t.Fatalf("unexpected source %+v", source)
		}
		roles := map[string]bool{}
		foundFeature := false
		for _, ref := range source.Refs {
			if !ref.Verified || ref.DestinationPresent {
				t.Fatalf("unexpected ref status %+v", ref)
			}
			if ref.RunID != "" {
				roles[ref.Role] = true
				if ref.RunID != runIDs[i] || !strings.HasPrefix(ref.DestinationRef, "refs/runs/"+ref.RunID+"/") {
					t.Fatal("lost run namespace")
				}
			}
			if ref.SourceRef == "refs/heads/feature" {
				foundFeature = true
				if ref.DestinationRef != "refs/lanes/"+source.RepoID+"/heads/feature" {
					t.Fatal("colliding migration branch names")
				}
			}
		}
		if !foundFeature || !roles["head"] || !roles["base"] || !roles["submitted"] {
			t.Fatalf("live run refs not fully checked: %+v", roles)
		}
	}
	if after := migrationSnapshot(t, p.Root()); !reflect.DeepEqual(before, after) {
		for path, value := range after {
			if before[path] != value {
				t.Errorf("dry-run changed %s: %s -> %s", path, before[path], value)
			}
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				t.Errorf("dry-run removed %s", path)
			}
		}
		t.Fatal("dry-run changed gate, registry, or store files")
	}
	if _, err := os.Stat(filepath.Join(p.Root(), "stores")); !os.IsNotExist(err) {
		t.Fatal("dry-run created destination")
	}
}

func TestMigrationRejectsMissingLiveCommitAndIncompleteObjectGraph(t *testing.T) {
	ctx := context.Background()
	work := setupTestRepo(t)
	// A blob, not just a commit tip, must be verified before any migration.
	if err := os.WriteFile(filepath.Join(work, "payload"), []byte("fixture payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Run(ctx, work, "add", "payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Run(ctx, work, "commit", "-m", "payload"); err != nil {
		t.Fatal(err)
	}
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	repo := legacyMigrationGate(t, p, d, work, "legacy")
	head, err := git.HeadSHA(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "refs/heads/feature", strings.Repeat("a", 40), head)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Verified || plan.Sources[0].Verified {
		t.Fatal("missing live run commit was accepted")
	}
	if err := d.UpdateRunHeadSHA(run.ID, head); err != nil {
		t.Fatal(err)
	}
	// The tip commit remains readable while a referenced blob goes missing.
	blob, err := git.Run(ctx, work, "rev-parse", "HEAD:payload")
	if err != nil {
		t.Fatal(err)
	}
	gate := p.RepoDir(repo.ID)
	if err := os.Remove(filepath.Join(gate, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, gate, "cat-file", "-e", head+"^{commit}"); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Verified || !strings.Contains(strings.Join(plan.Sources[0].Issues, " "), "connectivity") {
		t.Fatal("invalid branch object graph was accepted")
	}
}

func TestMigrationReportsSharedGatesAndOrphansWithoutChangingThem(t *testing.T) {
	ctx := context.Background()
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	repo, _, err := Init(ctx, d, p, work)
	if err != nil {
		t.Fatal(err)
	}
	head, err := git.HeadSHA(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "refs/heads/feature", head, head)
	if err != nil {
		t.Fatal(err)
	}
	if err := RetainRun(ctx, p, run); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Verified || plan.Sources[0].Action != "already-shared" {
		t.Fatalf("shared source %+v", plan.Sources[0])
	}
	for _, ref := range plan.Sources[0].Refs {
		if ref.RunID != "" && !ref.DestinationPresent {
			t.Fatal("existing live run pin was missed")
		}
	}
	orphan := p.RepoDir("orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	before := migrationSnapshot(t, p.Root())
	plan, err = PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Verified || len(plan.Issues) == 0 {
		t.Fatal("orphan silently ignored")
	}
	if after := migrationSnapshot(t, p.Root()); !reflect.DeepEqual(before, after) {
		t.Fatal("dry-run mutated orphan/shared gate")
	}
}

func TestMigrationAcceptsZeroBaseOnFirstBranchPush(t *testing.T) {
	ctx := context.Background()
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	repo := legacyMigrationGate(t, p, d, work, "legacy")
	head, err := git.HeadSHA(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertRun(repo.ID, "feature", head, strings.Repeat("0", 40)); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Verified {
		t.Fatalf("zero previous tip must not be treated as a missing commit: %+v", plan.Sources[0].Issues)
	}
}

func TestMigrationIncludesLiveDetachedWorktreeAndReviewRoundCommits(t *testing.T) {
	ctx := context.Background()
	work := setupTestRepo(t)
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	repo := legacyMigrationGate(t, p, d, work, "legacy")
	base, err := git.HeadSHA(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", base, base)
	if err != nil {
		t.Fatal(err)
	}
	wt := p.WorktreeDir(repo.ID, run.ID)
	if err := git.WorktreeAdd(ctx, p.RepoDir(repo.ID), wt, base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { git.WorktreeRemove(context.Background(), p.RepoDir(repo.ID), wt) })
	if _, err := git.Run(ctx, wt, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "unpersisted candidate"); err != nil {
		t.Fatal(err)
	}
	head, err := git.HeadSHA(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertReviewStepRoundWithProvenance(step.ID, 1, "initial", nil, nil, head, base, base, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Verified {
		t.Fatalf("unexpected issues: %+v", plan.Sources[0].Issues)
	}
	seen := map[string]bool{}
	for _, ref := range plan.Sources[0].Refs {
		seen[ref.Role] = true
		if ref.Role == "worktree-head" && ref.OID != head {
			t.Fatal("worktree candidate lost")
		}
	}
	for _, role := range []string{"worktree-head", "reviewed-round/" + step.ID + "/1", "starting-round/" + step.ID + "/1", "trusted-config/" + step.ID + "/1"} {
		if !seen[role] {
			t.Errorf("missing live role %s", role)
		}
	}
	if _, err := d.UpdateRepoMetadata(repo.ID, "https://other.example/project.git", "main"); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanMigration(ctx, d, p)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Verified || !strings.Contains(strings.Join(plan.Sources[0].Issues, " "), "upstream disagrees") {
		t.Fatal("mismatched registration was accepted")
	}
}
