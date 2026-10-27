package gate

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestInitSharesUpstreamObjectsAndKeepsLaneRefs(t *testing.T) {
	ctx := context.Background()
	first := setupTestRepo(t)
	second := filepath.Join(t.TempDir(), "second")
	if _, err := git.Run(ctx, first, "clone", first, second); err != nil {
		t.Fatal(err)
	}
	upstream, err := git.GetRemoteURL(ctx, first, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if err := git.EnsureRemote(ctx, second, "origin", upstream); err != nil {
		t.Fatal(err)
	}
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	one, _, err := Init(ctx, d, p, first)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := Init(ctx, d, p, second)
	if err != nil {
		t.Fatal(err)
	}
	if one.ID == two.ID {
		t.Fatal("lane registrations must remain distinct")
	}
	var shared string
	for _, id := range []string{one.ID, two.ID} {
		data, err := os.ReadFile(filepath.Join(p.RepoDir(id), "objects", "info", "alternates"))
		if err != nil {
			t.Fatalf("gate must borrow the upstream object store: %v", err)
		}
		objectDir := strings.TrimSpace(string(data))
		if shared != "" && objectDir != shared {
			t.Fatalf("same upstream has two object stores: %q and %q", shared, objectDir)
		}
		shared = objectDir
		head, err := git.Run(ctx, first, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := git.RunBare(ctx, p.RepoDir(id), "cat-file", "-e", head+"^{commit}"); err != nil {
			t.Fatalf("borrowed history unavailable: %v", err)
		}
		if _, err := os.Stat(filepath.Join(p.RepoDir(id), "objects", head[:2], head[2:])); !os.IsNotExist(err) {
			t.Fatal("upstream commit duplicated inside lane gate")
		}
	}
	head, _ := git.Run(ctx, first, "rev-parse", "HEAD")
	if _, err := git.RunBare(ctx, p.RepoDir(one.ID), "update-ref", "refs/heads/same-branch", head); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, p.RepoDir(two.ID), "show-ref", "--verify", "refs/heads/same-branch"); err == nil {
		t.Fatal("lane branch refs leaked across registrations")
	}
	if _, err := Eject(ctx, d, p, first); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, p.RepoDir(two.ID), "cat-file", "-e", head); err != nil {
		t.Fatalf("eject broke another lane: %v", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("eject removed shared store: %v", err)
	}
}

func TestUpstreamIdentityNormalization(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://EXAMPLE.com:443/team/project.git/", "https://example.com/team/project"},
		{"https://user:fixture-password@example.com/team/project.git", "https://example.com/team/project"},
		{"git@EXAMPLE.com:team/project.git", "ssh://git@example.com/team/project"},
		{"ssh://git@example.com:22/team/project/", "ssh://git@example.com/team/project"},
		{"example.com:team/project.git", "ssh://example.com/team/project"},
		{"https://example.com/Team/Project.git", "https://example.com/Team/Project"},
		{"ssh://user@example.com:2222/team/project.git", "ssh://user@example.com:2222/team/project"},
	} {
		got, err := UpstreamIdentity(t.TempDir(), tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("normalization got %q, %v; want %q", got, err, tc.want)
		}
	}
	root := t.TempDir()
	local := filepath.Join(root, "upstream.git")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := UpstreamIdentity(root, "./upstream.git")
	if err != nil {
		t.Fatal(err)
	}
	b, err := UpstreamIdentity(root, (&url.URL{Scheme: "file", Path: local}).String())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("file URL and relative path must share identity: %q %q", a, b)
	}
	for _, bad := range []string{"", "https://example.com/repo?credential=fixture", "https://example.com/repo#fragment", "https://example.com", "ext::command", "repo\nother"} {
		if _, err := UpstreamIdentity(root, bad); err == nil {
			t.Error("accepted malformed/unsupported upstream")
		}
	}
}

func TestRunRefsRetainIndependentLaneGenerations(t *testing.T) {
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
	base, err := git.Run(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gateDir := p.RepoDir(repo.ID)
	// This fixture tests object negotiation, not daemon admission. The
	// installed hooks name the go test executable, so remove them here.
	for _, name := range []string{"pre-receive", "post-receive"} {
		if err := os.Remove(filepath.Join(gateDir, "hooks", name)); err != nil {
			t.Fatal(err)
		}
	}
	store, err := UpstreamStoreDir(p, work, repo.UpstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"run-one", "run-two"} {
		if _, err := git.Run(ctx, work, "commit", "--allow-empty", "-m", id); err != nil {
			t.Fatal(err)
		}
		head, err := git.Run(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := git.Run(ctx, work, "-c", "core.hooksPath=", "push", "--force", gateDir, "HEAD:refs/heads/same-branch"); err != nil {
			t.Fatal(err)
		}
		// The history seeded at init must remain borrowed after receive-pack.
		if _, err := os.Stat(filepath.Join(gateDir, "objects", base[:2], base[2:])); !os.IsNotExist(err) {
			t.Fatal("receive-pack copied borrowed history")
		}
		counts, err := git.RunBare(ctx, gateDir, "count-objects", "-v")
		if err != nil {
			t.Fatal(err)
		}
		localCount := 0
		for _, line := range strings.Split(counts, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && (fields[0] == "count:" || fields[0] == "in-pack:") {
				n, err := strconv.Atoi(fields[1])
				if err != nil {
					t.Fatal(err)
				}
				localCount += n
			}
		}
		// Empty commits introduce exactly one new object each. Counting loose
		// AND packed objects catches a full baseline hidden inside a pack.
		if localCount != index+1 {
			t.Fatalf("lane holds %d objects after %d empty commits; shared history was duplicated", localCount, index+1)
		}
		run := &db.Run{ID: id, RepoID: repo.ID, HeadSHA: head, BaseSHA: base}
		if err := RetainRun(ctx, p, run); err != nil {
			t.Fatal(err)
		}
		if err := RetainRun(ctx, p, run); err != nil {
			t.Fatalf("retention not idempotent: %v", err)
		}
		if got, err := git.RunBare(ctx, store, "rev-parse", "refs/runs/"+id+"/"+head); err != nil || got != head {
			t.Fatalf("run ref missing: %v", err)
		}
	}
	if _, err := git.RunBare(ctx, store, "repack", "-ad"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, store, "gc", "--prune=now"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunBare(ctx, gateDir, "fsck", "--full", "--no-dangling"); err != nil {
		t.Fatalf("pool maintenance broke borrower: %v", err)
	}
	refs, err := git.RunBare(ctx, store, "for-each-ref", "--format=%(refname)", "refs/runs/")
	if err != nil || !strings.Contains(refs, "refs/runs/run-one/") || !strings.Contains(refs, "refs/runs/run-two/") {
		t.Fatalf("run generations lost: %v", err)
	}
}

func TestRetainRunAllowsZeroBaseOnFirstBranchPush(t *testing.T) {
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
	run, err := d.InsertRun(repo.ID, "feature", head, strings.Repeat("0", 40))
	if err != nil {
		t.Fatal(err)
	}
	if err := RetainRun(ctx, p, run); err != nil {
		t.Fatalf("new branch must retain its head without a previous tip: %v", err)
	}
}

func TestConcurrentLanesInitializeOneUpstreamPool(t *testing.T) {
	ctx := context.Background()
	first := setupTestRepo(t)
	second := filepath.Join(t.TempDir(), "second")
	if _, err := git.Run(ctx, first, "clone", first, second); err != nil {
		t.Fatal(err)
	}
	upstream, err := git.GetRemoteURL(ctx, first, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if err := git.EnsureRemote(ctx, second, "origin", upstream); err != nil {
		t.Fatal(err)
	}
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t, p)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, work := range []string{first, second} {
		go func(work string) { <-start; _, _, err := Init(ctx, d, p, work); results <- err }(work)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(p.Root(), "stores"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".git") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent initializers created %d upstream pools", count)
	}
}

func TestUpstreamIdentityAcceptsLiteralLocalPathCharacters(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "upstream%# repo.git")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	pathIdentity, err := UpstreamIdentity(root, local)
	if err != nil {
		t.Fatalf("literal local path: %v", err)
	}
	fileIdentity, err := UpstreamIdentity(root, (&url.URL{Scheme: "file", Path: local}).String())
	if err != nil {
		t.Fatal(err)
	}
	if pathIdentity != fileIdentity {
		t.Fatalf("local path and escaped file URL differ: %q %q", pathIdentity, fileIdentity)
	}
}
