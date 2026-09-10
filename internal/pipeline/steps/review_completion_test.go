package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
)

// The real executor and review step must advance on model return, even when
// completions happen between any hypothetical fixed polling boundaries.
func TestReviewCompletionStartsFixAndRereviewWithoutRoundTimer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := make(chan string, 1)
	returns := make(chan struct{})
	reviewRound := 0
	mock := &sessionMockAgent{respond: func(opts agent.RunOpts) *agent.Result {
		started <- opts.Purpose
		select {
		case <-returns:
		case <-ctx.Done():
		}
		if opts.Purpose == "review-fix" {
			return &agent.Result{Output: []byte(`{"summary":"address bug"}`)}
		}
		reviewRound++
		if reviewRound < 3 {
			return &agent.Result{Output: []byte(fmt.Sprintf(`{"findings":[{"id":"f-%d","severity":"error","description":"bug %d","action":"auto-fix"}],"summary":"issues","risk_level":"medium","risk_rationale":"bugs"}`, reviewRound, reviewRound))}
		}
		return &agent.Result{Output: []byte(`{"findings":[],"summary":"clean","risk_level":"low","risk_rationale":"clean"}`)}
	}}
	events := make(chan ipc.Event, 100)
	executor, database, run, repo, workDir := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}}, func(event ipc.Event) {
		if event.Type == ipc.EventStepCompleted {
			events <- event
		}
	})
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, run, repo, workDir) }()
	for _, want := range []string{"review", "review-fix", "review", "review-fix", "review"} {
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("turn %s, want %s", got, want)
			}
		case err := <-done:
			t.Fatalf("ended before %s: %v", want, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not start on prior completion", want)
		}
		select {
		case <-started:
			t.Fatal("next turn started before model returned")
		case <-time.After(17 * time.Millisecond):
		}
		select {
		case returns <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("executor did not consume model return")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(events)
	completions := 0
	for event := range events {
		if event.Type == ipc.EventStepCompleted {
			completions++
			t.Logf("observed %s: step=%s status=%s", event.Type, *event.StepName, *event.Status)
		}
	}
	if completions != 3 {
		t.Fatalf("completion events = %d, want two fixing transitions and final completion", completions)
	}
	timing, err := database.GetReviewTiming(run.ID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if timing == nil || !timing.Complete || timing.RoundCount != 3 || len(timing.Turns) != 5 || timing.ReviewMS < 45 || timing.FixMS < 30 {
		t.Fatalf("persisted completion timing: %+v", timing)
	}
}
