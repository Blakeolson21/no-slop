package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/convergence"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func convergenceGuardConfig(autoFixRounds int) *config.Config {
	return &config.Config{
		AutoFix: config.AutoFix{Review: autoFixRounds},
		Review: config.Review{Convergence: config.Convergence{
			NonDecreasingRounds: config.DefaultReviewConvergenceNonDecreasingRounds,
			RecurringRounds:     config.DefaultReviewConvergenceRecurringRounds,
			BudgetMinutes:       config.DefaultReviewConvergenceBudgetMinutes,
		}},
	}
}

func reviewFindingsJSONForRound(round int, descriptions ...string) string {
	items := ""
	for i, d := range descriptions {
		if i > 0 {
			items += ","
		}
		items += fmt.Sprintf(`{"id":"r%d-f%d","severity":"warning","file":"pkg%d.go","description":"%s","action":"auto-fix"}`, round, i+1, i+1, d)
	}
	return fmt.Sprintf(`{"findings":[%s],"summary":"round %d"}`, items, round)
}

// A review loop whose findings count never decreases must stop consuming
// auto-fix rounds and park for an explicit decision, with the convergence
// report persisted. The park is advisory: an explicit approve still resolves
// the gate.
func TestExecutor_ReviewLadderParksInsteadOfAutoFixing(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	cfg := convergenceGuardConfig(5)

	// Distinct descriptions per round so only the non-decreasing counts
	// trigger can fire, never the recurring-class trigger.
	descriptions := []string{
		"retry loop spins without backoff in the uploader",
		"stale cache entry served after invalidation in the session store",
		"missing timeout on the outbound webhook client",
		"partial write left behind when the disk fills during export",
	}
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			return &StepOutcome{
				AutoFixable: true,
				Findings:    reviewFindingsJSONForRound(callCount, descriptions[callCount-1]),
			}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, workDir) }()

	// Rounds 1 and 2 auto-fix normally; after round 3 the counts are 1,1,1
	// (non-decreasing across the window) and the guard parks instead of
	// spending auto-fix round 3 of 5.
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusFixReview)
	if callCount != 3 {
		t.Errorf("step calls = %d, want 3 (guard must stop funding auto-fix rounds)", callCount)
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("get steps: %v (%d steps)", err, len(steps))
	}
	if steps[0].ConvergenceJSON == nil {
		t.Fatal("parked review step should carry a persisted convergence report")
	}
	report, ok := convergence.ParseReport(*steps[0].ConvergenceJSON)
	if !ok || !report.Tripped() {
		t.Fatalf("persisted report should be tripped, got %v (ok=%v)", report, ok)
	}
	if got := convergence.FormatRoundCounts(report.RoundFindings); got != "1,1,1" {
		t.Fatalf("round history = %q, want 1,1,1", got)
	}

	// Advisory, not an abort: an explicit approve adjudicates and completes.
	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	updated, _ := database.GetRun(run.ID)
	if updated.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want completed", updated.Status)
	}
}

// The negative case that matters most: a converging loop (findings shrinking
// to zero) must never trip the guard and must auto-fix to completion exactly
// as before.
func TestExecutor_ConvergingReviewAutoFixesToCompletionWithoutTripping(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	cfg := convergenceGuardConfig(5)

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			switch callCount {
			case 1:
				return &StepOutcome{AutoFixable: true, Findings: reviewFindingsJSONForRound(1,
					"nil pointer dereference when the config file is missing",
					"unchecked error return from file close in the writer",
					"data race on the shared counter in the worker pool",
				)}, nil
			case 2:
				return &StepOutcome{AutoFixable: true, Findings: reviewFindingsJSONForRound(2,
					"the new fallback path swallows the parse error silently",
					"flush can run twice when retry fires during shutdown",
				)}, nil
			default:
				return &StepOutcome{}, nil
			}
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if callCount != 3 {
		t.Errorf("step calls = %d, want 3 (two auto-fix rounds then clean)", callCount)
	}
	updated, _ := database.GetRun(run.ID)
	if updated.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want completed", updated.Status)
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("get steps: %v (%d steps)", err, len(steps))
	}
	if steps[0].ConvergenceJSON == nil {
		t.Fatal("review step should persist convergence telemetry even when healthy")
	}
	report, ok := convergence.ParseReport(*steps[0].ConvergenceJSON)
	if !ok {
		t.Fatalf("persisted report unparseable: %q", *steps[0].ConvergenceJSON)
	}
	if report.Tripped() {
		t.Fatalf("converging run tripped the guard: %q", report.Warning)
	}
	if got := convergence.FormatRoundCounts(report.RoundFindings); got != "3,2,0" {
		t.Fatalf("round history = %q, want 3,2,0", got)
	}
}

// Non-review steps are outside the guard's scope: a lint ladder keeps today's
// auto-fix budget semantics.
func TestExecutor_GuardDoesNotApplyToNonReviewSteps(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	cfg := &config.Config{
		AutoFix: config.AutoFix{Lint: 4},
		Review: config.Review{Convergence: config.Convergence{
			NonDecreasingRounds: 3, RecurringRounds: 3, BudgetMinutes: 120,
		}},
	}

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepLint,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount <= 4 {
				return &StepOutcome{AutoFixable: true, Findings: reviewFindingsJSONForRound(callCount,
					"line length exceeds the configured limit in the generated table")}, nil
			}
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if callCount != 5 {
		t.Errorf("lint step calls = %d, want 5 (guard must not throttle non-review steps)", callCount)
	}
	steps, _ := database.GetStepsByRun(run.ID)
	if len(steps) == 1 && steps[0].ConvergenceJSON != nil {
		t.Fatal("non-review steps must not carry convergence reports")
	}
}
