package cli

import (
	"context"
	"encoding/json"
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

type resumeFixture struct {
	dir       string
	p         *paths.Paths
	database  *db.DB
	run       *db.Run
	step      *db.StepResult
	candidate string
}

func setupResumeFixture(t *testing.T) resumeFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	dir, p, database, repo := setupAxiQueryRepo(t)
	t.Chdir(dir)
	f := resumeFixture{dir: dir, p: p, database: database}
	f.candidate = f.commit(t, "source.go", "package source\n")
	var err error
	f.run, err = database.InsertRun(repo.ID, "feature", f.candidate, f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(f.run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	f.step, err = database.InsertStepResult(f.run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	f.park(t, []types.Finding{{ID: "error-1", Severity: "error", File: "source.go", Description: "fix source", Action: types.ActionAskUser}})
	return f
}

func (f resumeFixture) commit(t *testing.T, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, f.dir, "git", "add", "--", name)
	run(t, f.dir, "git", "commit", "-m", "change")
	sha, err := git.HeadSHA(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func (f resumeFixture) park(t *testing.T, findings []types.Finding) {
	t.Helper()
	raw, err := json.Marshal(types.Findings{Items: findings})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if err := f.database.ParkStepForApproval(f.run.ID, f.step.ID, types.StepStatusParkedForApproval, 1, &s); err != nil {
		t.Fatal(err)
	}
}

func TestAxiResumeCheckUsesStoredCandidateAndDoesNotMutate(t *testing.T) {
	f := setupResumeFixture(t)
	head := f.commit(t, "source.go", "package source\n// repaired\n")
	// Neither the live clone HEAD nor the mutable run head is the candidate.
	later := f.commit(t, "unrelated.txt", "later local work\n")
	if err := f.database.UpdateRunHeadSHA(f.run.ID, later); err != nil {
		t.Fatal(err)
	}
	beforeRun, _ := f.database.GetRun(f.run.ID)
	beforeStep, _ := f.database.GetStepResult(f.step.ID)
	beforeHead, _ := git.HeadSHA(context.Background(), f.dir)
	out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	for _, want := range []string{"scope-checked", f.candidate, head, "source.go", "resume_available: false", "test_receipt_reused: false"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	afterRun, _ := f.database.GetRun(f.run.ID)
	afterStep, _ := f.database.GetStepResult(f.step.ID)
	afterHead, _ := git.HeadSHA(context.Background(), f.dir)
	if !reflect.DeepEqual(beforeRun, afterRun) || !reflect.DeepEqual(beforeStep, afterStep) || beforeHead != afterHead {
		t.Fatal("scope check changed run, review, or clone HEAD")
	}
	if _, err := os.Stat(f.p.Socket()); !os.IsNotExist(err) {
		t.Fatalf("scope check created daemon socket: %v", err)
	}
}

func TestAxiResumeWithoutCheckRefusesBeforeOpeningState(t *testing.T) {
	t.Setenv("NS_HOME", filepath.Join(t.TempDir(), "absent"))
	out, err := executeCmd("axi", "resume", "--run", "run", "--head", strings.Repeat("a", 40))
	if err == nil || !strings.Contains(out, "in-place resume is not available") {
		t.Fatalf("got %v, %s", err, out)
	}
	if _, err := os.Stat(os.Getenv("NS_HOME")); !os.IsNotExist(err) {
		t.Fatalf("created state: %v", err)
	}
}

func TestAxiResumeCheckRefusesUnsafeScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, resumeFixture) string
		want    string
	}{
		{"outside error files", func(t *testing.T, f resumeFixture) string {
			return f.commit(t, "other.go", "package other\n")
		}, "not named by a review error"},
		{"warning does not authorize", func(t *testing.T, f resumeFixture) string {
			f.park(t, []types.Finding{{Severity: "warning", File: "source.go"}})
			return f.commit(t, "source.go", "package changed\n")
		}, "no error findings naming files"},
		{"rename destination outside scope", func(t *testing.T, f resumeFixture) string {
			run(t, f.dir, "git", "mv", "source.go", "renamed.go")
			return f.commit(t, "renamed.go", "package source\n")
		}, "not named by a review error"},
		{"rename source outside scope", func(t *testing.T, f resumeFixture) string {
			f.park(t, []types.Finding{{Severity: "error", File: "renamed.go"}})
			run(t, f.dir, "git", "mv", "source.go", "renamed.go")
			return f.commit(t, "renamed.go", "package source\n")
		}, "not named by a review error"},
		{"sibling", func(t *testing.T, f resumeFixture) string {
			run(t, f.dir, "git", "checkout", "-b", "sibling", f.candidate+"^")
			return f.commit(t, "source.go", "package repaired\n")
		}, "cannot prove head descends"},
		{"unchanged head", func(t *testing.T, f resumeFixture) string { return f.candidate }, "no file changes"},
		{"mutable ref", func(t *testing.T, f resumeFixture) string { return "HEAD" }, "full commit SHA"},
		{"missing object", func(t *testing.T, f resumeFixture) string { return strings.Repeat("f", 40) }, "unavailable"},
		{"malformed findings", func(t *testing.T, f resumeFixture) string {
			if err := f.database.SetStepFindings(f.step.ID, "{"); err != nil {
				t.Fatal(err)
			}
			return f.commit(t, "source.go", "package repaired\n")
		}, "decode stored review findings"},
		{"executing review", func(t *testing.T, f resumeFixture) string {
			if err := f.database.UpdateStepStatus(f.step.ID, types.StepStatusRunning); err != nil {
				t.Fatal(err)
			}
			return f.commit(t, "source.go", "package repaired\n")
		}, "parked at review"},
		{"terminal run", func(t *testing.T, f resumeFixture) string {
			if err := f.database.UpdateRunStatus(f.run.ID, types.RunFailed); err != nil {
				t.Fatal(err)
			}
			return f.commit(t, "source.go", "package repaired\n")
		}, "running and parked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupResumeFixture(t)
			head := tc.prepare(t, f)
			out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
			if err == nil || !strings.Contains(out, tc.want) || strings.Contains(out, "scope-checked") {
				t.Fatalf("want refusal %q; got %v, %s", tc.want, err, out)
			}
		})
	}
}

func TestAxiResumeCheckPrefersApprovedCandidate(t *testing.T) {
	f := setupResumeFixture(t)
	approved := f.commit(t, "approved.go", "package approved\n")
	if err := f.database.UpdateRunReviewApprovedHeadSHA(f.run.ID, approved); err != nil {
		t.Fatal(err)
	}
	head := f.commit(t, "source.go", "package repaired\n")
	out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err != nil || !strings.Contains(out, approved) || strings.Contains(out, "approved.go") {
		t.Fatalf("did not scope repair against approved candidate: %v, %s", err, out)
	}
	// An unavailable approval must never silently fall back to the submission.
	if err := f.database.UpdateRunReviewApprovedHeadSHA(f.run.ID, strings.Repeat("f", 40)); err != nil {
		t.Fatal(err)
	}
	out, err = executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err == nil || !strings.Contains(out, "unavailable") {
		t.Fatalf("unavailable approved candidate accepted: %v, %s", err, out)
	}
}

func TestAxiResumeCheckDoesNotAcceptReplacementAncestry(t *testing.T) {
	f := setupResumeFixture(t)
	run(t, f.dir, "git", "checkout", "-b", "sibling", f.candidate+"^")
	sibling := f.commit(t, "source.go", "package sibling\n")
	run(t, f.dir, "git", "checkout", "--detach", f.candidate)
	descendant := f.commit(t, "source.go", "package descendant\n")
	run(t, f.dir, "git", "replace", sibling, descendant)
	// The ordinary Git view now claims the sibling descends from the candidate.
	run(t, f.dir, "git", "merge-base", "--is-ancestor", f.candidate, sibling)
	out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", sibling)
	if err == nil || !strings.Contains(out, "cannot prove head descends") {
		t.Fatalf("replacement ancestry accepted: %v, %s", err, out)
	}
}

func TestAxiResumeCheckKeepsExactFileNames(t *testing.T) {
	f := setupResumeFixture(t)
	name := " source.go"
	f.park(t, []types.Finding{{Severity: "error", File: name}})
	head := f.commit(t, name, "package source\n")
	out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err != nil || !strings.Contains(out, "scope-checked") {
		t.Fatalf("exact file name rejected: %v, %s", err, out)
	}
	f.park(t, []types.Finding{{Severity: "error", File: strings.TrimSpace(name)}})
	out, err = executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err == nil || !strings.Contains(out, "not named by a review error") {
		t.Fatalf("trimmed file name authorized another file: %v, %s", err, out)
	}
}

func TestAxiResumeCheckRejectsOtherRepositoryRun(t *testing.T) {
	f := setupResumeFixture(t)
	repo, err := f.database.InsertRepoWithID("other", t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.database.InsertRun(repo.ID, "feature", f.candidate, f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeCmd("axi", "resume", "--check", "--run", other.ID, "--head", f.candidate)
	if err == nil || !strings.Contains(out, "current registered repository") {
		t.Fatalf("wrong repository accepted: %v, %s", err, out)
	}
}

func TestAxiResumeCheckCannotHideSubmoduleChanges(t *testing.T) {
	f := setupResumeFixture(t)
	run(t, f.dir, "git", "update-index", "--add", "--cacheinfo", "160000,"+f.candidate+",dependency")
	run(t, f.dir, "git", "commit", "-m", "add gitlink")
	approved, err := git.HeadSHA(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateRunReviewApprovedHeadSHA(f.run.ID, approved); err != nil {
		t.Fatal(err)
	}
	run(t, f.dir, "git", "update-index", "--cacheinfo", "160000,"+approved+",dependency")
	head := f.commit(t, "source.go", "package repaired\n")
	run(t, f.dir, "git", "config", "diff.ignoreSubmodules", "all")
	out, err := executeCmd("axi", "resume", "--check", "--run", f.run.ID, "--head", head)
	if err == nil || !strings.Contains(out, "dependency") || !strings.Contains(out, "not named by a review error") {
		t.Fatalf("hidden submodule change accepted: %v, %s", err, out)
	}
}
