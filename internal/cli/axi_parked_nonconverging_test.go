package cli

// Compatibility tests for the parked non-converging state.
//
// The convergence guard is advisory by design: a tripped report never creates
// a distinct terminal outcome. Instead the review step parks at its approval
// gate (fix_review) with the tripped report persisted on it, the run stays
// non-terminal and parked awaiting-agent, and the driving agent is handed the
// gate as an explicit decision point. These tests pin that the same parked
// state is presented coherently across every surface:
//
//   - status render: the run object carries awaiting_agent and the tripped
//     report, and no terminal outcome line;
//   - database replay: the parked state survives close/reopen, and a
//     head-change replay reset keeps the review's tripped report while
//     invalidating later steps;
//   - CLI exit behavior: a drive that stops at the tripped gate exits 0 with
//     the gate doc, never as a passed/failed outcome.
//
// A regression that turned the parked state into a terminal outcome (or hid
// the park) fails here without any new state type being introduced.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
)

// parkedNonconvergingRun builds the IPC snapshot a daemon emits for a run
// whose review loop tripped the convergence guard and parked at its gate.
func parkedNonconvergingRun() *ipc.RunInfo {
	since := nowUnix() - 90
	tripped := trippedConvergenceJSON()
	findings := `{"findings":[{"id":"r3-1","severity":"warning","file":"a.go","action":"auto-fix","description":"env parsing diverges"}],"summary":"3 findings"}`
	return &ipc.RunInfo{
		ID:                 "run-parked",
		RepoID:             "repo-1",
		Branch:             "feature/ladder",
		HeadSHA:            "abc12345def67890",
		BaseSHA:            "000000000000",
		Status:             types.RunRunning,
		AwaitingAgent:      true,
		AwaitingAgentSince: &since,
		Steps: []ipc.StepResultInfo{{
			ID:              "step-review",
			RunID:           "run-parked",
			StepName:        types.StepReview,
			StepOrder:       types.StepReview.Order(),
			Status:          types.StepStatusFixReview,
			FindingsJSON:    &findings,
			ConvergenceJSON: &tripped,
		}},
	}
}

// A run parked by the convergence guard is not a terminal outcome: one status
// read must show the parked marker and the tripped report, and must not show
// an outcome line that would read as passed or failed.
func TestStatusRendersParkedNonconvergingAsAwaitingAgent(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/ladder", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	step, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert review step: %v", err)
	}
	if err := database.UpdateStepStatus(step.ID, types.StepStatusFixReview); err != nil {
		t.Fatalf("park review step: %v", err)
	}
	if err := database.SetStepFindings(step.ID, findingsJSON(t, []types.Finding{
		{ID: "r3-1", Severity: "warning", File: "a.go", Action: types.ActionAutoFix, Description: "env parsing diverges"},
	}, "3 findings")); err != nil {
		t.Fatalf("set findings: %v", err)
	}
	if err := database.SetStepConvergence(step.ID, trippedConvergenceJSON()); err != nil {
		t.Fatalf("set convergence: %v", err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatalf("mark run awaiting agent: %v", err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, run.ID); err != nil {
		t.Fatalf("axi status --run: %v\n%s", err, out.String())
	}
	got := out.String()

	if !strings.Contains(got, "awaiting_agent: parked") {
		t.Errorf("status must show the run parked awaiting the agent:\n%s", got)
	}
	if !strings.Contains(got, "review loop is not converging") {
		t.Errorf("status must carry the tripped convergence warning:\n%s", got)
	}
	if hasTerminalOutcomeField(got) {
		t.Errorf("a parked run must not render a terminal outcome line:\n%s", got)
	}
}

// hasTerminalOutcomeField reports whether the rendered TOON doc carries a
// top-level `outcome:` field. The standard gate help mentions the word inside
// a quoted value, which is not a field.
func hasTerminalOutcomeField(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "outcome:") {
			return true
		}
	}
	return false
}

// A daemon restart replays the run from the database: the parked nonconverging
// state must survive close/reopen unchanged, and a head-change replay reset
// must keep the review's tripped report while invalidating the steps after it.
func TestParkedNonconvergingSurvivesReopenAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo, err := database.InsertRepo("/tmp/park-replay", "https://example.com/park-replay.git", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	run, err := database.InsertRun(repo.ID, "feature/ladder", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	review, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert review step: %v", err)
	}
	if err := database.UpdateStepStatus(review.ID, types.StepStatusFixReview); err != nil {
		t.Fatalf("park review step: %v", err)
	}
	if err := database.SetStepConvergence(review.ID, trippedConvergenceJSON()); err != nil {
		t.Fatalf("set convergence: %v", err)
	}
	testStep, err := database.InsertStepResult(run.ID, types.StepTest)
	if err != nil {
		t.Fatalf("insert test step: %v", err)
	}
	if err := database.UpdateStepStatus(testStep.ID, types.StepStatusCompleted); err != nil {
		t.Fatalf("complete test step: %v", err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatalf("mark run awaiting agent: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	reopened, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	gotRun, err := reopened.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run after reopen: %v", err)
	}
	if gotRun.Status != types.RunRunning {
		t.Errorf("run status after reopen = %s, want still running (the park is not a terminal outcome)", gotRun.Status)
	}
	if gotRun.AwaitingAgentSince == nil {
		t.Errorf("awaiting_agent_since must survive reopen, got nil")
	}
	gotReview, err := reopened.GetStepResult(review.ID)
	if err != nil {
		t.Fatalf("get review step after reopen: %v", err)
	}
	if gotReview.Status != types.StepStatusFixReview {
		t.Errorf("review status after reopen = %s, want fix_review", gotReview.Status)
	}
	if gotReview.ConvergenceJSON == nil || *gotReview.ConvergenceJSON != trippedConvergenceJSON() {
		t.Errorf("tripped report must survive reopen, got %v", gotReview.ConvergenceJSON)
	}

	// A replay reset from the step after review invalidates the test step but
	// preserves the review's findings truth, including the tripped report the
	// next review round extends rather than loses.
	if err := reopened.ResetStepsFromOrder(run.ID, types.StepTest.Order()); err != nil {
		t.Fatalf("reset steps from test order: %v", err)
	}
	gotReview, err = reopened.GetStepResult(review.ID)
	if err != nil {
		t.Fatalf("get review step after reset: %v", err)
	}
	if gotReview.ConvergenceJSON == nil || *gotReview.ConvergenceJSON != trippedConvergenceJSON() {
		t.Errorf("replay reset must preserve the review's tripped report, got %v", gotReview.ConvergenceJSON)
	}
	gotTest, err := reopened.GetStepResult(testStep.ID)
	if err != nil {
		t.Fatalf("get test step after reset: %v", err)
	}
	if gotTest.Status != types.StepStatusPending || gotTest.ConvergenceJSON != nil {
		t.Errorf("test step after reset = status %s convergence %v, want pending with no report", gotTest.Status, gotTest.ConvergenceJSON)
	}
}

// The drive CLI must hand a convergence-parked run back as a decision point:
// exit 0 with the gate doc carrying the tripped report and the parked marker,
// never a passed/failed outcome.
func TestRenderDriveResult_ParkedNonconvergingExitsZeroWithGateDoc(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	if err := renderDriveResult(cmd, parkedNonconvergingRun(), false); err != nil {
		t.Fatalf("a run parked by the convergence guard is a decision point, not a failure; drive must exit 0, got: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"awaiting_agent: parked",
		"convergence:",
		"review loop is not converging",
		// The guard demotes fix from being the obvious next step.
		"Do not respond `fix` by default",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("drive output missing %q in:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"outcome: passed", "outcome: failed", "exit_code: 1"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("parked nonconverging output must not carry terminal signal %q:\n%s", forbidden, got)
		}
	}
}
