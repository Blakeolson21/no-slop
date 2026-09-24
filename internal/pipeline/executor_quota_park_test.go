package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutorParksQuotaRefusalWithResetTime(t *testing.T) {
	database, p, run, repo := setupTest(t)
	reset := time.Date(2026, 9, 26, 15, 57, 0, 0, time.UTC)
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		return nil, &agent.LaneOutageError{Lane: "local_codex_exec/gpt-reserve/reserve", Until: reset, Reason: "You've hit your usage limit"}
	}}
	exec := NewExecutor(database, p, &config.Config{}, nil, []Step{step}, nil)
	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, t.TempDir()) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil || len(steps) != 1 || steps[0].FindingsJSON == nil {
		t.Fatalf("parked findings: steps=%#v err=%v", steps, err)
	}
	if !strings.Contains(*steps[0].FindingsJSON, "2026-09-26") || !strings.Contains(*steps[0].FindingsJSON, "local_codex_exec") {
		t.Fatalf("park omitted route or reset time: %s", *steps[0].FindingsJSON)
	}
	if err := exec.Respond(types.StepReview, types.ActionSkip, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("park resolution: %v", err)
	}
}

func TestExecutorQuotaParkApprovalRetriesTheUnexecutedStep(t *testing.T) {
	database, p, run, repo := setupTest(t)
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		calls++
		if calls == 1 {
			return nil, &agent.LaneOutageError{Lane: "route=local_codex_exec model=gpt-reserve seat=/seats/reserve", Until: time.Now().Add(time.Hour)}
		}
		return &StepOutcome{ReviewApprovedHeadSHA: run.HeadSHA}, nil
	}}}
	exec := NewExecutor(database, p, &config.Config{}, nil, []Step{step}, nil)
	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, t.TempDir()) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("approval certified an unexecuted review: calls=%d", calls)
	}
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("completed steps: %#v error=%v", steps, err)
	}
	if steps[0].FindingsJSON != nil && strings.Contains(*steps[0].FindingsJSON, "quota-exhausted") {
		t.Fatalf("resolved quota diagnostic remained as an active finding: %s", *steps[0].FindingsJSON)
	}
}
