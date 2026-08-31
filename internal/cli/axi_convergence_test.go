package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
)

func trippedConvergenceJSON() string {
	return `{
		"round_findings":[1,1,1,2,3,3,3],
		"review_ms":11520000,
		"new_surface_findings":4,
		"recurring":[{"label":"env-parse-semantic","rounds":[4,6,7],"files":["restore.sh","backup.sh","convex-env.sh"]}],
		"warning":"review loop is not converging: findings per round have not decreased across the last 3 rounds (3,3,3)"
	}`
}

// A gate return carries the full per-round convergence history so a
// non-converging loop is visible without manually tallying rounds across
// separate tool invocations.
func TestGateRendersConvergenceHistory(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "fix_review",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r7-1", Severity: "error", File: "convex-env.sh", Action: types.ActionAskUser, Description: "env parsing diverges"},
		}, "3 findings"),
		ConvergenceJSON: trippedConvergenceJSON(),
	}
	out := axiDoc(gateFields(gate)...)

	for _, want := range []string{
		"convergence:",
		"rounds:",
		"1,1,1,2,3,3,3",
		"review_time: 3h12m",
		"findings_outside_submitted_diff: 4",
		"env-parse-semantic (rounds 4,6,7)",
		"review loop is not converging",
		// The guard demotes fix from being the obvious next step.
		"Do not respond `fix` by default",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("gate missing %q in:\n%s", want, out)
		}
	}
	// The warning guidance leads the help block: it must come before the
	// standard fix instruction.
	warnIdx := strings.Index(out, "Do not respond `fix` by default")
	fixIdx := strings.Index(out, "--action fix --findings")
	if warnIdx == -1 || fixIdx == -1 || warnIdx > fixIdx {
		t.Errorf("convergence guidance should precede the fix instruction (warn=%d fix=%d):\n%s", warnIdx, fixIdx, out)
	}
}

// A healthy report renders history without the warning or the demotion note.
func TestGateRendersHealthyConvergenceWithoutWarning(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "awaiting_approval",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r1-1", Severity: "warning", File: "a.go", Action: types.ActionAskUser, Description: "x"},
		}, "1 finding"),
		ConvergenceJSON: `{"round_findings":[3,1],"review_ms":60000}`,
	}
	out := axiDoc(gateFields(gate)...)
	if !strings.Contains(out, "rounds:") || !strings.Contains(out, "3,1") {
		t.Errorf("healthy gate should still carry round history:\n%s", out)
	}
	if strings.Contains(out, "warning:") || strings.Contains(out, "Do not respond `fix` by default") {
		t.Errorf("healthy gate must not carry the convergence warning:\n%s", out)
	}
}

// Gates without a persisted report (legacy runs, non-review steps) render
// exactly as before.
func TestGateWithoutConvergenceReportUnchanged(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "awaiting_approval",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "review-1", Severity: "warning", File: "main.go", Action: types.ActionAskUser, Description: "x"},
		}, "1 finding"),
	}
	out := axiDoc(gateFields(gate)...)
	if strings.Contains(out, "convergence:") {
		t.Errorf("gate without a report must not render a convergence block:\n%s", out)
	}
}

// An explicit status read keeps the persisted review convergence history
// visible after the review gate is no longer the active approval surface.
func TestAxiStatusRunRendersPersistedConvergence(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/convergence", "head", "base")
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
	if err := database.UpdateStepStatus(step.ID, types.StepStatusCompleted); err != nil {
		t.Fatalf("complete review step: %v", err)
	}
	if err := database.SetStepConvergence(step.ID, trippedConvergenceJSON()); err != nil {
		t.Fatalf("set convergence: %v", err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, run.ID); err != nil {
		t.Fatalf("axi status --run: %v\n%s", err, out.String())
	}

	for _, want := range []string{
		"  convergence:\n",
		"    rounds: \"1,1,1,2,3,3,3\"\n",
		"    review_time: 3h12m\n",
		"review loop is not converging",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("axi status --run missing %q in:\n%s", want, out.String())
		}
	}
}

// Status must distinguish a run whose review has not produced convergence
// state yet from a real zero-valued report.
func TestAxiStatusRunRendersUnknownConvergenceWhenAbsent(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/no-convergence-yet", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	if _, err := database.InsertStepResult(run.ID, types.StepReview); err != nil {
		t.Fatalf("insert review step: %v", err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, run.ID); err != nil {
		t.Fatalf("axi status --run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "  convergence: unknown\n") {
		t.Fatalf("axi status --run must render absent convergence as unknown:\n%s", out.String())
	}
}

// While the review gate is the active approval surface it already renders the
// report, so status must not repeat the same history inside the run object.
func TestAxiStatusRunDoesNotDuplicateActiveReviewGateConvergence(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/active-review-gate", "head", "base")
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
	if err := database.UpdateStepStatus(step.ID, types.StepStatusAwaitingApproval); err != nil {
		t.Fatalf("park review step: %v", err)
	}
	if err := database.SetStepConvergence(step.ID, trippedConvergenceJSON()); err != nil {
		t.Fatalf("set convergence: %v", err)
	}

	out := axiStatusOutput(t, run.ID)
	if got := strings.Count(out, "rounds: \"1,1,1,2,3,3,3\""); got != 1 {
		t.Fatalf("round history should be rendered exactly once, got %d:\n%s", got, out)
	}
	gateIdx := strings.Index(out, "gate:")
	convIdx := strings.Index(out, "convergence:")
	if gateIdx == -1 || convIdx == -1 || convIdx < gateIdx {
		t.Fatalf("the single convergence block must belong to the gate (gate=%d convergence=%d):\n%s", gateIdx, convIdx, out)
	}
	if strings.Contains(out, "convergence: unknown") {
		t.Fatalf("an active gate report must not also render as unknown:\n%s", out)
	}
}

// The non-duplication rule is keyed on the review step holding the gate, not
// on the run merely being parked somewhere: a gate on another step leaves the
// review report unrendered unless status projects it.
func TestAxiStatusRunProjectsConvergenceWhenAnotherStepHoldsTheGate(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/test-gate", "head", "base")
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
	if err := database.UpdateStepStatus(review.ID, types.StepStatusCompleted); err != nil {
		t.Fatalf("complete review step: %v", err)
	}
	if err := database.SetStepConvergence(review.ID, trippedConvergenceJSON()); err != nil {
		t.Fatalf("set convergence: %v", err)
	}
	test, err := database.InsertStepResult(run.ID, types.StepTest)
	if err != nil {
		t.Fatalf("insert test step: %v", err)
	}
	if err := database.UpdateStepStatus(test.ID, types.StepStatusAwaitingApproval); err != nil {
		t.Fatalf("park test step: %v", err)
	}

	out := axiStatusOutput(t, run.ID)
	if !strings.Contains(out, "rounds: \"1,1,1,2,3,3,3\"") {
		t.Fatalf("review convergence must stay visible while another step holds the gate:\n%s", out)
	}
	gateIdx := strings.Index(out, "gate:")
	convIdx := strings.Index(out, "convergence:")
	if gateIdx == -1 || convIdx == -1 || convIdx > gateIdx {
		t.Fatalf("convergence should be projected into run, above the test gate (gate=%d convergence=%d):\n%s", gateIdx, convIdx, out)
	}
}

// A corrupt persisted report is indistinguishable from no measurement, so it
// must read as unknown and never as a zero-valued report.
func TestAxiStatusRunRendersUnreadableConvergenceAsUnknown(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	run, err := database.InsertRun(repo.ID, "feature/corrupt-convergence", "head", "base")
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
	if err := database.UpdateStepStatus(step.ID, types.StepStatusCompleted); err != nil {
		t.Fatalf("complete review step: %v", err)
	}
	if err := database.SetStepConvergence(step.ID, "{not-json"); err != nil {
		t.Fatalf("set convergence: %v", err)
	}

	out := axiStatusOutput(t, run.ID)
	if !strings.Contains(out, "  convergence: unknown\n") {
		t.Fatalf("an unreadable report must render as unknown:\n%s", out)
	}
	for _, forbidden := range []string{"rounds:", "review_time:"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("unknown convergence must not render measured fields (%q):\n%s", forbidden, out)
		}
	}
}

// axiStatusOutput runs `axi status --run <id>` and returns what an operator
// would see on stdout.
func axiStatusOutput(t *testing.T, runID string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, runID); err != nil {
		t.Fatalf("axi status --run: %v\n%s", err, out.String())
	}
	return out.String()
}

// --yes must stop auto-funding fix rounds the moment the guard trips: a
// non-converging gate is handed back as an explicit decision point.
func TestGateResolution_ConvergenceTrippedParksInsteadOfFix(t *testing.T) {
	actionable := findingsJSON(t, []types.Finding{
		{ID: "r3-1", Severity: "warning", File: "a.go", Action: types.ActionAutoFix, Description: "x"},
	}, "1 finding")

	tripped := stepView{Name: "review", Status: "fix_review", FindingsJSON: actionable, ConvergenceJSON: trippedConvergenceJSON()}
	if _, _, resolved := gateResolution(tripped, 1); resolved {
		t.Fatal("tripped guard with actionable findings must not be auto-resolved")
	}

	healthy := stepView{Name: "review", Status: "fix_review", FindingsJSON: actionable, ConvergenceJSON: `{"round_findings":[3,1],"review_ms":60000}`}
	action, ids, resolved := gateResolution(healthy, 1)
	if !resolved || action != types.ActionFix || len(ids) != 1 {
		t.Fatalf("healthy gate should fund a fix as before, got action=%v ids=%v resolved=%v", action, ids, resolved)
	}

	// A tripped guard on a gate whose findings are all non-actionable is
	// still approved: the loop is ending, there is nothing to adjudicate.
	clean := stepView{
		Name: "review", Status: "fix_review",
		FindingsJSON:    findingsJSON(t, []types.Finding{{ID: "n1", Severity: "info", Action: types.ActionNoOp, Description: "note"}}, "clean"),
		ConvergenceJSON: trippedConvergenceJSON(),
	}
	action, _, resolved = gateResolution(clean, 1)
	if !resolved || action != types.ActionApprove {
		t.Fatalf("non-actionable gate should approve even when tripped, got action=%v resolved=%v", action, resolved)
	}
}
