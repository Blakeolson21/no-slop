package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestRespondReceiptPreventsRetryFromFundingAnotherRound(t *testing.T) {
	database, p, run, repo := setupTest(t)
	gates := make(chan struct{}, 4)
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[],"summary":"needs review"}`}, nil
	}}
	executor := NewExecutor(database, p, nil, nil, []Step{step}, func(event ipc.Event) {
		if event.Status != nil && (*event.Status == string(types.StepStatusAwaitingApproval) || *event.Status == string(types.StepStatusFixReview)) {
			gates <- struct{}{}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, run, repo, t.TempDir()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("executor did not stop")
		}
	})
	waitGate := func() {
		t.Helper()
		select {
		case <-gates:
		case <-time.After(5 * time.Second):
			t.Fatal("gate did not open")
		}
	}
	waitGate()
	params := ipc.RespondParams{RunID: run.ID, Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "ruling-8"}
	first, err := executor.AcceptResponse(params)
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK || first.Replayed || first.RunID != run.ID || first.Round != 1 {
		t.Fatalf("acceptance = %+v", first)
	}
	// Discard the acknowledgement, as a client whose socket timed out would.
	waitGate()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := executor.AcceptResponse(params)
			if err != nil {
				t.Error(err)
				return
			}
			if !got.Replayed || got.Round != first.Round {
				t.Errorf("replay = %+v", got)
			}
		}()
	}
	wg.Wait()
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("retry funded another round: %d rounds", len(rounds))
	}
	params.Instructions = map[string]string{"f1": "different ruling"}
	if _, err := executor.AcceptResponse(params); err == nil || !strings.Contains(err.Error(), "different response") {
		t.Fatalf("key conflict = %v", err)
	}
	params.Instructions = nil
	// A replacement executor has no in-memory receipt or waiting state.
	replacement := NewExecutor(database, p, nil, nil, nil, nil)
	got, err := replacement.AcceptResponse(params)
	if err != nil || !got.Replayed || got.Round != 1 {
		t.Fatalf("durable replay = %+v, %v", got, err)
	}
}
