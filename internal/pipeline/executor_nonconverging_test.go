package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/convergence"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutor_NonconvergenceTerminatesWithoutResponder(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		maxRounds, recurring, wantRounds int
	}{
		{"round cap", 5, 0, 5},
		{"recurring class", 0, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, p, run, repo := setupTest(t)
			raw, err := config.LoadRepoFromBytes([]byte(fmt.Sprintf(`auto_fix:
  review: 10
review:
  convergence:
    non_decreasing_rounds: 0
    recurring_rounds: 0
    budget_minutes: 0
    max_rounds: %d
    max_recurring_rounds: %d
`, tc.maxRounds, tc.recurring)))
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Merge(config.DefaultGlobalConfig(), raw)
			calls := 0
			step := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
				calls++
				if calls > tc.wantRounds {
					return &StepOutcome{}, nil
				}
				// Different IDs and files, same defect class; credentials must not reach the ticket.
				findings := fmt.Sprintf(`{"findings":[{"id":"r%d","severity":"warning","file":"pkg%d.go","description":"missing timeout on outbound webhook client https://user:secret@example.com/repo","action":"auto-fix"}]}`, calls, calls)
				return &StepOutcome{AutoFixable: true, Findings: findings}, nil
			}}
			downstream := 0
			next := &adaptiveCallStep{name: types.StepTest, fn: func(*StepContext) (*StepOutcome, error) { downstream++; return &StepOutcome{}, nil }}
			ex := NewExecutor(database, p, cfg, nil, []Step{step, next}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = ex.Execute(ctx, run, repo, t.TempDir())
			if ctx.Err() != nil {
				t.Fatal("executor waited for a responder instead of stopping")
			}
			if err == nil {
				t.Fatal("non-converging review completed successfully")
			}
			stored, err := database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != "parked-nonconverging" || calls != tc.wantRounds || downstream != 0 {
				t.Fatalf("status=%s calls=%d downstream=%d", stored.Status, calls, downstream)
			}
			if stored.TerminalAtMS == nil {
				t.Fatal("terminal timestamp missing")
			}
			if stored.ReviewApprovedHeadSHA != nil {
				t.Fatal("non-converging review authorized push")
			}
			if err := ex.Respond(types.StepReview, types.ActionFix, nil); err == nil {
				t.Fatal("terminal run accepted another fix")
			}
			steps, err := database.GetStepsByRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if steps[0].Status != types.StepStatusFailed || steps[0].ConvergenceJSON == nil {
				t.Fatalf("review result: %+v", steps[0])
			}
			report, ok := convergence.ParseReport(*steps[0].ConvergenceJSON)
			if !ok || len(report.RoundFindings) != tc.wantRounds {
				t.Fatalf("report: %+v", report)
			}
			// The durable report locates the complete ticket without inflating IPC.
			ticket, err := os.ReadFile(report.RedesignTicketPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(ticket)
			if !strings.Contains(text, "Acceptance seeds") || !strings.Contains(text, "pkg1.go") || !strings.Contains(text, fmt.Sprintf("pkg%d.go", tc.wantRounds)) {
				t.Fatalf("missing accumulated ticket findings: %s", text)
			}
			if strings.Contains(text, "user:secret") {
				t.Fatal("ticket leaked URL credentials")
			}
			log, err := os.ReadFile(p.RunLogDir(run.ID) + "/review.log")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), report.RedesignTicketPath) {
				t.Fatal("log does not locate ticket")
			}

		})
	}
}

func TestExecutor_NonconvergenceCapsResponderFixes(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{Review: config.Review{Convergence: config.Convergence{MaxRounds: 2}}}
	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		calls++
		return &StepOutcome{NeedsApproval: true, Findings: reviewFindingsJSONForRound(calls, "outbound webhook client has no timeout")}, nil
	}}
	ex := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ex.Execute(ctx, run, repo, t.TempDir()) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	if err := ex.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNonconverging) {
		t.Fatalf("execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	stored, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != types.RunParkedNonconverging || stored.AwaitingAgentSince != nil {
		t.Fatalf("run: %+v", stored)
	}
}
