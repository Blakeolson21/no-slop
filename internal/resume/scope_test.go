package resume

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestCheckScopeIgnoresLegacyGrafts(t *testing.T) {
	dir := newScopeTestRepo(t)
	gitTest(t, dir, "commit", "--allow-empty", "-m", "initial")
	candidate := writeScopeCommit(t, dir, "source.go", "package source\n")
	gitTest(t, dir, "checkout", "-b", "sibling", candidate+"^")
	head := writeScopeCommit(t, dir, "source.go", "package sibling\n")

	grafts := filepath.Join(dir, ".git", "info", "grafts")
	if err := os.MkdirAll(filepath.Dir(grafts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grafts, []byte(head+" "+candidate+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "merge-base", "--is-ancestor", candidate, head)

	if _, err := CheckScope(context.Background(), dir, scopeRun(candidate), scopeReview(), head); err == nil || !strings.Contains(err.Error(), "cannot prove head descends") {
		t.Fatalf("grafted sibling passed the ancestry check: %v", err)
	}
}

func TestCheckScopeDoesNotFetchMissingPartialCloneObjects(t *testing.T) {
	source := newScopeTestRepo(t)
	candidate := writeScopeCommit(t, source, "source.go", "package source\n")
	candidateTree := gitTest(t, source, "rev-parse", candidate+"^{tree}")
	head := writeScopeCommit(t, source, "source.go", "package source\n// repaired\n")
	headTree := gitTest(t, source, "rev-parse", head+"^{tree}")

	origin := filepath.Join(t.TempDir(), "origin.git")
	gitTest(t, source, "clone", "--bare", source, origin)
	gitTest(t, origin, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	cloneURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(origin)}).String()
	gitTest(t, source, "clone", "--filter=tree:0", "--no-checkout", cloneURL, partial)
	assertScopeTestObjectMissing(t, partial, candidateTree)
	assertScopeTestObjectMissing(t, partial, headTree)

	tracePath := filepath.Join(t.TempDir(), "git-trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", tracePath)
	_, err := CheckScope(context.Background(), partial, scopeRun(candidate), scopeReview(), head)
	if err == nil || !strings.Contains(err.Error(), "compare candidate to head") {
		t.Fatalf("scope check should refuse when trees are absent locally; got %v", err)
	}
	assertScopeTestObjectMissing(t, partial, candidateTree)
	assertScopeTestObjectMissing(t, partial, headTree)

	trace, readErr := os.ReadFile(tracePath)
	if readErr != nil {
		t.Fatalf("read Git trace: %v", readErr)
	}
	if strings.Contains(string(trace), `"child_class":"transport"`) || strings.Contains(string(trace), "git-upload-pack") {
		t.Fatalf("scope check contacted its promisor remote:\n%s", trace)
	}
}

func newScopeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "test@example.com")
	gitTest(t, dir, "config", "user.name", "Scope Test")
	return dir
}

func writeScopeCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", name)
	gitTest(t, dir, "commit", "-m", "scope test change")
	return gitTest(t, dir, "rev-parse", "HEAD")
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := git.Run(ctx, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func assertScopeTestObjectMissing(t *testing.T, dir, objectID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := git.RunWithEnv(ctx, dir, []string{"GIT_NO_LAZY_FETCH=1"}, "cat-file", "-e", objectID); err == nil {
		t.Fatalf("object %s unexpectedly exists in partial clone", objectID)
	}
}

func scopeRun(candidate string) *db.Run {
	return &db.Run{ID: "scope-test", Status: types.RunRunning, SubmittedHeadSHA: &candidate}
}

func scopeReview() *db.StepResult {
	findings, _ := json.Marshal(types.Findings{Items: []types.Finding{{Severity: "error", File: "source.go"}}})
	raw := string(findings)
	return &db.StepResult{
		RunID:        "scope-test",
		StepName:     types.StepReview,
		Status:       types.StepStatusParkedForApproval,
		FindingsJSON: &raw,
	}
}
