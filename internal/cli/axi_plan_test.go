package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/gate"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func setupAxiPlanRepo(t *testing.T) (*paths.Paths, string) {
	t.Helper()
	repoDir := setupTestRepo(t)
	run(t, repoDir, "git", "switch", "-c", "feature/effective-config")

	p := paths.WithRoot(os.Getenv("NS_HOME"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: codex\nquartermaster:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := gate.Init(context.Background(), database, p, repoDir); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	linkTestBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p, repoDir
}

func decodeAxiRunPlan(t *testing.T, out string) axiRunPlan {
	t.Helper()
	var plan axiRunPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan JSON %q: %v", out, err)
	}
	return plan
}

func TestAxiPlanUsesTheRunCommandsPflagGrammarForBooleanFalse(t *testing.T) {
	setupAxiPlanRepo(t)
	argv := []string{"--yes=false", "--skip", "test,lint", "--intent", `preserve spaces and "quotes"`}
	out, err := executeCmd(append([]string{"axi", "plan"}, argv...)...)
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)

	backend := newAxiRunCmd()
	if err := backend.ParseFlags(argv); err != nil {
		t.Fatalf("real axi run flag set rejected argv: %v", err)
	}
	wantYes, err := backend.Flags().GetBool("yes")
	if err != nil {
		t.Fatal(err)
	}
	wantSkipValue, err := backend.Flags().GetString("skip")
	if err != nil {
		t.Fatal(err)
	}
	wantSkip, err := parseSkipSteps(wantSkipValue)
	if err != nil {
		t.Fatal(err)
	}
	wantIntent, err := backend.Flags().GetString("intent")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Yes != wantYes || plan.Yes {
		t.Fatalf("plan yes = %v, backend yes = %v; --yes=false must remain false", plan.Yes, wantYes)
	}
	if !reflect.DeepEqual(plan.Skip, wantSkip) {
		t.Fatalf("plan skip = %v, backend skip = %v", plan.Skip, wantSkip)
	}
	if plan.Intent != wantIntent {
		t.Fatalf("plan intent = %q, backend intent = %q", plan.Intent, wantIntent)
	}
}

func TestAxiPlanUsesTheRunCommandsPflagTerminatorSemantics(t *testing.T) {
	setupAxiPlanRepo(t)
	argv := []string{"--yes", "--", "--skip", "push"}
	out, err := executeCmd(append([]string{"axi", "plan"}, argv...)...)
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)

	backend := newAxiRunCmd()
	if err := backend.ParseFlags(argv); err != nil {
		t.Fatalf("real axi run flag set rejected argv: %v", err)
	}
	wantYes, _ := backend.Flags().GetBool("yes")
	wantSkipValue, _ := backend.Flags().GetString("skip")
	wantSkip, err := parseSkipSteps(wantSkipValue)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Yes != wantYes || !plan.Yes {
		t.Fatalf("plan yes = %v, backend yes = %v", plan.Yes, wantYes)
	}
	if !slices.Equal(plan.Skip, wantSkip) || len(plan.Skip) != 0 {
		t.Fatalf("plan skip = %v, backend skip = %v; post-terminator --skip became a flag", plan.Skip, wantSkip)
	}
	if !reflect.DeepEqual(plan.PositionalArgs, backend.Flags().Args()) {
		t.Fatalf("plan positional args = %q, backend positional args = %q", plan.PositionalArgs, backend.Flags().Args())
	}
	wantArgs := []string{"--skip", "push"}
	if !reflect.DeepEqual(plan.PositionalArgs, wantArgs) {
		t.Fatalf("post-terminator args = %q, want positional %q", plan.PositionalArgs, wantArgs)
	}
}

func TestAxiPlanMatchesRunGrammarForRepeatedAndEmptyFlags(t *testing.T) {
	setupAxiPlanRepo(t)
	argv := []string{"--yes", "--yes=false", "--skip=test", "--skip=", "--intent=first", "--intent", `last "quoted" intent`}
	out, err := executeCmd(append([]string{"axi", "plan"}, argv...)...)
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)

	backend := newAxiRunCmd()
	if err := backend.ParseFlags(argv); err != nil {
		t.Fatalf("real axi run flag set rejected argv: %v", err)
	}
	wantYes, _ := backend.Flags().GetBool("yes")
	wantSkipValue, _ := backend.Flags().GetString("skip")
	wantSkip, err := parseSkipSteps(wantSkipValue)
	if err != nil {
		t.Fatal(err)
	}
	wantIntent, _ := backend.Flags().GetString("intent")
	if plan.Yes != wantYes || !slices.Equal(plan.Skip, wantSkip) || plan.Intent != wantIntent {
		t.Fatalf("plan parsed (%v, %v, %q), backend parsed (%v, %v, %q)", plan.Yes, plan.Skip, plan.Intent, wantYes, wantSkip, wantIntent)
	}
	if plan.Yes || len(plan.Skip) != 0 || plan.Intent != `last "quoted" intent` {
		t.Fatalf("last repeated values did not win: %#v", plan)
	}
}

func TestAxiPlanFailsLoudlyWithoutPartialJSON(t *testing.T) {
	setupAxiPlanRepo(t)
	for _, argv := range [][]string{
		{"--unknown"},
		{"--skip", "not-a-step"},
		{"--skip"},
	} {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			out, err := executeCmd(append([]string{"axi", "plan"}, argv...)...)
			if err == nil {
				t.Fatalf("axi plan %q exited 0 with %q", argv, out)
			}
			if out != "" {
				t.Fatalf("failed plan emitted partial stdout %q", out)
			}
			if !strings.Contains(err.Error(), "parse proposed axi run invocation") {
				t.Fatalf("failure did not name the proposed invocation: %v", err)
			}
		})
	}
}

func TestAxiPlanReportsQuartermasterPoolWithoutLeasingASeat(t *testing.T) {
	p, _ := setupAxiPlanRepo(t)
	marker := filepath.Join(t.TempDir(), "quartermaster-was-called")
	missingBin := filepath.Join(marker, "quartermaster")
	configYAML := "agent: codex\nquartermaster:\n  enabled: true\n  bin: " + strconv.Quote(missingBin) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeCmd("axi", "plan", "--intent", "inspect only")
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)
	if len(plan.Agent.Lanes) != 1 {
		t.Fatalf("lanes = %#v, want one", plan.Agent.Lanes)
	}
	seat := plan.Agent.Lanes[0].Seat
	if seat.Source != "quartermaster" || seat.Pool != "codex" || seat.Selection != "deferred-until-invocation" {
		t.Fatalf("seat = %#v, want deferred codex quartermaster selection", seat)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("plan invoked the deliberately missing lease authority; marker stat error = %v", err)
	}
}

func TestAxiPlanResolvesTheRegisteredRepoFromASubdirectory(t *testing.T) {
	_, repoDir := setupAxiPlanRepo(t)
	subdir := filepath.Join(repoDir, "nested", "directory")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, subdir)

	out, err := executeCmd("axi", "plan", "--intent", "inspect from below the root")
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)
	if plan.Repo.Path != repoDir || plan.Branch != "feature/effective-config" {
		t.Fatalf("target = repo %#v branch %q, want root %q", plan.Repo, plan.Branch, repoDir)
	}
}

func TestAxiPlanReportsResolvedTargetAndAgentWithoutRunSideEffects(t *testing.T) {
	p, repoDir := setupAxiPlanRepo(t)
	beforeHead := gitOutput(t, repoDir, "rev-parse", "HEAD")
	beforeStatus := gitOutput(t, repoDir, "status", "--porcelain=v1")

	database, err := db.OpenReadOnly(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repos, err := database.GetRepos()
	if err != nil || len(repos) != 1 {
		_ = database.Close()
		t.Fatalf("registered repos = %d, %v", len(repos), err)
	}
	beforeRuns, err := database.GetRunsByRepo(repos[0].ID)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	beforeWorktreeRefs := gitOutput(t, repoDir, "for-each-ref", "--format=%(refname) %(objectname)")
	beforeGateRefs := gitBareOutput(t, p.RepoDir(repos[0].ID), "for-each-ref", "--format=%(refname) %(objectname)")

	var first string
	for i := 0; i < 2; i++ {
		out, err := executeCmd("axi", "plan", "--yes", "--skip=push,pr,ci", "--intent", "inspect only")
		if err != nil {
			t.Fatalf("axi plan pass %d failed: %v\n%s", i+1, err, out)
		}
		if i == 0 {
			first = out
		} else if out != first {
			t.Fatalf("identical plans differ:\nfirst: %s\nsecond: %s", first, out)
		}
	}
	plan := decodeAxiRunPlan(t, first)
	if plan.Repo.ID != repos[0].ID || plan.Repo.Path != repoDir || plan.Branch != "feature/effective-config" {
		t.Fatalf("target = repo %#v branch %q, want id %q path %q branch feature/effective-config", plan.Repo, plan.Branch, repos[0].ID, repoDir)
	}
	if plan.Agent.Primary != types.AgentCodex || !reflect.DeepEqual(plan.Agent.Fallbacks, []types.AgentName{types.AgentCodex}) {
		t.Fatalf("agent = %#v, want resolved codex", plan.Agent)
	}
	if len(plan.Agent.Lanes) != 1 || plan.Agent.Lanes[0].Seat.Source != "process-environment" {
		t.Fatalf("agent lanes = %#v, want non-leased process environment", plan.Agent.Lanes)
	}

	database, err = db.OpenReadOnly(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := database.GetRunsByRepo(repos[0].ID)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRuns, beforeRuns) {
		t.Fatalf("plan changed run rows: before %#v after %#v", beforeRuns, afterRuns)
	}
	if entries, err := os.ReadDir(p.WorktreesDir()); err != nil || len(entries) != 0 {
		t.Fatalf("plan left managed worktrees: %v, %v", entries, err)
	}
	if got := gitOutput(t, repoDir, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("plan moved HEAD from %s to %s", beforeHead, got)
	}
	if got := gitOutput(t, repoDir, "status", "--porcelain=v1"); got != beforeStatus {
		t.Fatalf("plan changed worktree status from %q to %q", beforeStatus, got)
	}
	if got := gitOutput(t, repoDir, "for-each-ref", "--format=%(refname) %(objectname)"); got != beforeWorktreeRefs {
		t.Fatalf("plan changed worktree refs:\nbefore:\n%s\nafter:\n%s", beforeWorktreeRefs, got)
	}
	if got := gitBareOutput(t, p.RepoDir(repos[0].ID), "for-each-ref", "--format=%(refname) %(objectname)"); got != beforeGateRefs {
		t.Fatalf("plan changed gate refs:\nbefore:\n%s\nafter:\n%s", beforeGateRefs, got)
	}
	if _, err := os.Stat(p.Socket()); !os.IsNotExist(err) {
		t.Fatalf("plan contacted or started daemon; socket stat error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(p.WorktreesDir(), "*", "*")); err != nil || len(matches) != 0 {
		t.Fatalf("plan registered worktrees: %v, %v", matches, err)
	}
}

func gitOutput(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func gitBareOutput(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"--git-dir=" + gitDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git --git-dir=%s %v: %v", gitDir, args, err)
	}
	return strings.TrimSpace(string(out))
}
