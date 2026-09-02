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
	"github.com/Blakeolson21/no-slop/internal/identity"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
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
		t.Fatalf("decode plan JSON %q: %v", scrubPlanTestCredential(out), err)
	}
	return plan
}

// planTestCredential is the fabricated secret the redaction tests plant in the
// registered upstream and in the worktree's origin remote.
const planTestCredential = "s3cr3t-plan-token"

// scrubPlanTestCredential strips the planted secret from a diagnostic. It never
// calls the redaction code under test, so a failure report stays safe even when
// that code is the thing that is broken.
func scrubPlanTestCredential(text string) string {
	return strings.ReplaceAll(text, planTestCredential, "<credential>")
}

// assertNoPlanTestCredential is the single owner of the "this surface must not
// carry the credential" check. Test output is itself a published surface, so
// the failure it raises reports the offending text with the secret removed
// rather than reproducing what the assertion exists to keep out of sight.
func assertNoPlanTestCredential(t *testing.T, surface, text string) {
	t.Helper()
	if strings.Contains(text, planTestCredential) {
		t.Fatalf("%s leaked the planted credential: %s", surface, scrubPlanTestCredential(text))
	}
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
	if plan.Yes != wantYes {
		t.Fatalf("plan yes = %v, real run flag set yes = %v", plan.Yes, wantYes)
	}
	if plan.Yes {
		t.Fatal("plan yes = true; --yes=false must remain false")
	}
	if !reflect.DeepEqual(plan.Skip, wantSkip) {
		t.Fatalf("plan skip = %v, real run flag set skip = %v", plan.Skip, wantSkip)
	}
	if plan.Intent != wantIntent {
		t.Fatalf("plan intent = %q, real run flag set intent = %q", plan.Intent, wantIntent)
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
	if plan.Yes != wantYes {
		t.Fatalf("plan yes = %v, real run flag set yes = %v", plan.Yes, wantYes)
	}
	if !plan.Yes {
		t.Fatal("plan yes = false; --yes before the terminator must stay true")
	}
	if !slices.Equal(plan.Skip, wantSkip) {
		t.Fatalf("plan skip = %v, real run flag set skip = %v", plan.Skip, wantSkip)
	}
	if len(plan.Skip) != 0 {
		t.Fatalf("plan skip = %v; post-terminator --skip became a flag", plan.Skip)
	}
	if !reflect.DeepEqual(plan.PositionalArgs, backend.Flags().Args()) {
		t.Fatalf("plan positional args = %q, real run flag set positional args = %q", plan.PositionalArgs, backend.Flags().Args())
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
	if plan.Yes != wantYes {
		t.Fatalf("plan yes = %v, real run flag set yes = %v", plan.Yes, wantYes)
	}
	if !slices.Equal(plan.Skip, wantSkip) {
		t.Fatalf("plan skip = %v, real run flag set skip = %v", plan.Skip, wantSkip)
	}
	if plan.Intent != wantIntent {
		t.Fatalf("plan intent = %q, real run flag set intent = %q", plan.Intent, wantIntent)
	}
	if plan.Yes {
		t.Fatal("plan yes = true; the last repeated --yes=false must win")
	}
	if len(plan.Skip) != 0 {
		t.Fatalf("plan skip = %v; the last repeated empty --skip must win", plan.Skip)
	}
	if plan.Intent != `last "quoted" intent` {
		t.Fatalf("plan intent = %q; the last repeated --intent must win", plan.Intent)
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

func TestAxiPlanEnforcesTheRunFlagSetsOwnValidationNotJustItsParser(t *testing.T) {
	newProposalCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return nil }}
		cmd.Flags().String("intent", "", "")
		cmd.Flags().Bool("yes", false, "")
		cmd.Flags().Bool("no", false, "")
		if err := cmd.MarkFlagRequired("intent"); err != nil {
			t.Fatal(err)
		}
		cmd.MarkFlagsMutuallyExclusive("yes", "no")
		return cmd
	}
	if err := acceptProposedRunInvocation(newProposalCmd(), []string{"--yes"}); err == nil {
		t.Fatal("proposal missing a required run flag was accepted")
	}
	if err := acceptProposedRunInvocation(newProposalCmd(), []string{"--intent", "x", "--yes", "--no"}); err == nil {
		t.Fatal("proposal violating a run flag group was accepted")
	}
	if err := acceptProposedRunInvocation(newProposalCmd(), []string{"--intent", "x", "--yes"}); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
}

func TestAxiPlanServesItsOwnHelpAndKeepsItPositionalAfterTheTerminator(t *testing.T) {
	setupAxiPlanRepo(t)
	for _, argv := range [][]string{{"--help"}, {"-h"}} {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			out, err := executeCmd(append([]string{"axi", "plan"}, argv...)...)
			if err != nil {
				t.Fatalf("axi plan %q failed instead of showing usage: %v\n%s", argv, err, out)
			}
			if !strings.Contains(out, "no-slop axi plan") {
				t.Fatalf("axi plan %q did not print its own usage: %q", argv, out)
			}
			if strings.Contains(out, `"head_sha"`) {
				t.Fatalf("help request emitted a plan report: %q", out)
			}
		})
	}

	out, err := executeCmd("axi", "plan", "--", "--help")
	if err != nil {
		t.Fatalf("axi plan -- --help failed: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)
	backend := newAxiRunCmd()
	if err := backend.ParseFlags([]string{"--", "--help"}); err != nil {
		t.Fatalf("real axi run flag set rejected argv: %v", err)
	}
	if !reflect.DeepEqual(plan.PositionalArgs, backend.Flags().Args()) {
		t.Fatalf("plan positional args = %q, real run flag set positional args = %q", plan.PositionalArgs, backend.Flags().Args())
	}
	if !reflect.DeepEqual(plan.PositionalArgs, []string{"--help"}) {
		t.Fatalf("post-terminator --help = %q, want positional %q", plan.PositionalArgs, []string{"--help"})
	}
}

func TestAxiPlanRedactsRegisteredUpstreamCredentials(t *testing.T) {
	p, _ := setupAxiPlanRepo(t)
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repos, err := database.GetRepos()
	if err != nil || len(repos) != 1 {
		_ = database.Close()
		t.Fatalf("registered repos = %d, %v", len(repos), err)
	}
	if _, err := database.UpdateRepoMetadata(repos[0].ID, "https://plan-user:"+planTestCredential+"@example.com/owner/repo.git", repos[0].DefaultBranch); err != nil {
		_ = database.Close()
		t.Fatalf("register credentialled upstream: %v", scrubPlanTestCredential(err.Error()))
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := executeCmd("axi", "plan", "--intent", "inspect only")
	if err != nil {
		t.Fatalf("axi plan failed: %v\n%s", scrubPlanTestCredential(err.Error()), scrubPlanTestCredential(out))
	}
	assertNoPlanTestCredential(t, "plan report", out)
	plan := decodeAxiRunPlan(t, out)
	if plan.Repo.Upstream != "https://redacted@example.com/owner/repo.git" {
		t.Fatalf("reported upstream = %q, want the credential replaced", scrubPlanTestCredential(plan.Repo.Upstream))
	}
}

func TestAxiPlanFailureNeverEchoesOriginCredentials(t *testing.T) {
	_, repoDir := setupAxiPlanRepo(t)
	credentialURL := "https://plan-user:" + planTestCredential + "@example.invalid/owner/repo.git"
	// Set the remote without the shared run helper, which echoes its argv on
	// failure and would put the credential in the test log.
	setOrigin := exec.Command("git", "-C", repoDir, "remote", "set-url", "origin", credentialURL)
	if out, err := setOrigin.CombinedOutput(); err != nil {
		t.Fatalf("set credentialled origin: %v\n%s", err, scrubPlanTestCredential(string(out)))
	}
	// Refuse the transport locally so the failing fetch is offline and
	// deterministic while still carrying the credentialled URL in its argv.
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")

	out, err := executeCmd("axi", "plan", "--intent", "inspect only")
	if err == nil {
		t.Fatalf("axi plan succeeded against an unreachable origin: %s", scrubPlanTestCredential(out))
	}
	if out != "" {
		t.Fatalf("failed plan emitted partial stdout %q", scrubPlanTestCredential(out))
	}
	assertNoPlanTestCredential(t, "plan failure", err.Error())
	// The credentialled URL reaches the failing fetch's argv, so the error
	// naming its redacted form is what separates "redaction rewrote it" from
	// "the URL never surfaced at all", which would satisfy the absence
	// assertion above without proving anything.
	if !strings.Contains(err.Error(), "redacted@example.invalid") {
		t.Fatalf("plan failure did not name the redacted unreachable origin: %v", scrubPlanTestCredential(err.Error()))
	}
}

// TestAxiPlanWithoutATrustedConfigDoesNotAdoptThePushedBranchesAgent pins the
// absent-trusted-config merge path: the trusted default branch carries no repo
// config, so the pushed branch's code-executing agent selection must be
// discarded in favor of the operator's global lane rather than panicking or
// letting the proposed branch pick the process the run would launch.
func TestAxiPlanWithoutATrustedConfigDoesNotAdoptThePushedBranchesAgent(t *testing.T) {
	_, repoDir := setupAxiPlanRepo(t)
	pushedConfig := filepath.Join(repoDir, identity.RepoConfigName)
	if err := os.WriteFile(pushedConfig, []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repoDir, "git", "add", identity.RepoConfigName)
	run(t, repoDir, "git", "commit", "-m", "pushed branch selects its own agent")

	out, err := executeCmd("axi", "plan", "--intent", "inspect only")
	if err != nil {
		t.Fatalf("axi plan failed with no trusted config on the default branch: %v\n%s", err, out)
	}
	plan := decodeAxiRunPlan(t, out)
	if plan.Agent.Primary != types.AgentCodex {
		t.Fatalf("agent primary = %q, want the global %q; an absent trusted config must not let the pushed branch select the lane", plan.Agent.Primary, types.AgentCodex)
	}
	if !reflect.DeepEqual(plan.Agent.Fallbacks, []types.AgentName{types.AgentCodex}) {
		t.Fatalf("agent fallbacks = %#v, want only the global codex lane", plan.Agent.Fallbacks)
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
