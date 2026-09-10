package steps

import (
	"context"
	"errors"
	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
	"time"
)

func TestRealReviewFixRetainsCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixing := make(chan struct{})
	finish := make(chan struct{})
	round := 0
	mock := &sessionMockAgent{respond: func(opts agent.RunOpts) *agent.Result {
		if opts.Purpose == "review-fix" {
			close(fixing)
			select {
			case <-finish:
			case <-ctx.Done():
			}
			return &agent.Result{Output: []byte(`{"summary":"address bug"}`)}
		}
		round++
		if round == 1 {
			return &agent.Result{Output: []byte(`{"findings":[{"id":"f","severity":"error","description":"bug","action":"auto-fix"}],"risk_level":"medium","risk_rationale":"bug"}`)}
		}
		return &agent.Result{Output: []byte(`{"findings":[],"risk_level":"low","risk_rationale":"clean"}`)}
	}}
	e, _, run, repo, work := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}})
	c := pipeline.NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})
	e.SetCapacity(c)
	done := make(chan error, 1)
	go func() { done <- e.Execute(ctx, run, repo, work) }()
	select {
	case <-fixing:
	case <-ctx.Done():
		t.Fatal("fix did not start")
	}
	probe, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if release, err := c.Acquire(probe, types.StepReview); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("fix failed to hold review slot: %v", err)
	}
	release, err := c.Acquire(ctx, types.StepTest)
	if err != nil {
		t.Fatal(err)
	}
	release()
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
