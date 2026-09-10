package pipeline

import (
	"context"
	"errors"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
	"time"
)

func TestCapacityPreservesCancellationCause(t *testing.T) {
	for _, c := range []*Capacity{nil, NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})} {
		for _, step := range []types.StepName{types.StepReview, types.StepDocument} {
			ctx, cancel := context.WithCancelCause(context.Background())
			cause := errors.New("cancelled: superseded by new push")
			cancel(cause)
			_, err := c.Acquire(ctx, step)
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause for %s: %v", step, err)
			}
		}
	}
	c := NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})
	release, err := c.Acquire(context.Background(), types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("queued review superseded")
	done := make(chan error, 1)
	go func() { _, err := c.Acquire(ctx, types.StepReview); done <- err }()
	cancel(cause)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("queued cause lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not exit")
	}
}
func TestSingleSuiteCapacityAllowsIndependentReview(t *testing.T) {
	c := NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	suite, err := c.Acquire(ctx, types.StepTest)
	if err != nil {
		t.Fatal(err)
	}
	defer suite()
	review, err := c.Acquire(ctx, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	review()
	probe, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if release, err := c.Acquire(probe, types.StepLint); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("second suite admitted: %v", err)
	}
	suite()
	release, err := c.Acquire(ctx, types.StepLint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
