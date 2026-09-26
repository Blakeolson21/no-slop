package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutor_SeatRefusalBlocksRunAndStep(t *testing.T) {
	database, p, run, repo := setupTest(t)
	refusal := &agent.QuartermasterRefusalError{
		Pool:    "codex",
		Purpose: "review",
		Reason:  "no seat available",
	}
	step := newFailStep(types.StepReview, &agent.SeatBlockedError{Refusal: refusal})
	events := &eventCollector{}
	exec := NewExecutor(database, p, nil, nil, []Step{step}, events.handler)

	if err := exec.Execute(context.Background(), run, repo, t.TempDir()); err != nil {
		t.Fatalf("seat refusal should be a blocked outcome, got error: %v", err)
	}

	gotRun, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Status != types.RunStatus("blocked") {
		t.Fatalf("run status = %q, want blocked", gotRun.Status)
	}
	if gotRun.Error == nil || !strings.Contains(*gotRun.Error, "no seat available") {
		t.Fatalf("run error = %v, want the seat refusal reason", gotRun.Error)
	}
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != types.StepStatus("blocked") {
		t.Fatalf("step results = %+v, want one blocked step", steps)
	}
	if event := events.findLast(ipc.EventStepStatusChanged, "blocked"); event == nil {
		t.Fatal("missing blocked step status event")
	}
	if event := events.findRunEvent(ipc.EventRunCompleted); event == nil || event.Status == nil || *event.Status != "blocked" {
		t.Fatalf("terminal run event = %+v, want blocked", event)
	}
}
