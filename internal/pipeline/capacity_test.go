package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// This is an admission measurement, not a model-throughput benchmark. A held
// suite must leave all nine default review slots available, while independent
// daemon owners must not inherit each other's occupancy.
func TestDefaultCapacityOccupancyAndIndependentDaemons(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-unbounded", true: "separate-pools"}[bounded], func(t *testing.T) {
			var capacity *Capacity
			want := 12
			if bounded {
				capacity = NewCapacity(config.DefaultConcurrency())
				want = 9
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			suite, err := capacity.Acquire(ctx, types.StepTest)
			if err != nil {
				t.Fatal(err)
			}
			defer suite()
			started := make(chan struct{}, 12)
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					release, err := capacity.Acquire(ctx, types.StepReview)
					if err != nil {
						return
					}
					defer release()
					started <- struct{}{}
					<-ctx.Done()
				}()
			}
			defer func() { cancel(); wg.Wait() }()
			for range want {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("review pool did not fill")
				}
			}
			select {
			case <-started:
				t.Fatal("review capacity exceeded")
			case <-time.After(30 * time.Millisecond):
			}
			other := NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})
			release, err := other.Acquire(ctx, types.StepReview)
			if err != nil {
				t.Fatal("another daemon inherited occupancy:", err)
			}
			release()
			t.Logf("reviews in flight=%d with suites in flight=1; independent daemon admits its own review", want)
		})
	}
}

func TestCapacitySeparatesReviewsFromSuitesAndWakesOnRelease(t *testing.T) {
	c := NewCapacity(config.Concurrency{Reviews: 2, Suites: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r1, err := c.Acquire(ctx, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := c.Acquire(ctx, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	suite, err := c.Acquire(ctx, types.StepTest)
	if err != nil {
		t.Fatal("reviews consumed suite capacity:", err)
	}
	defer suite()
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Acquire(cancelled, types.StepLint); !errors.Is(err, context.Canceled) {
		t.Fatalf("lint must share suite capacity and honor cancellation: %v", err)
	}
	ready := make(chan func(), 1)
	go func() {
		release, err := c.Acquire(ctx, types.StepReview)
		if err == nil {
			ready <- release
		}
	}()
	select {
	case release := <-ready:
		release()
		t.Fatal("exceeded review cap")
	case <-time.After(30 * time.Millisecond):
	}
	r1()
	select {
	case release := <-ready:
		release()
	case <-ctx.Done():
		t.Fatal("release did not wake review waiter")
	}
	r1() // release is idempotent
}

func TestExecutorReleasesReviewCapacityWhileParked(t *testing.T) {
	database, p, run, repo := setupTest(t)
	c := NewCapacity(config.Concurrency{Reviews: 1, Suites: 1})
	executor := NewExecutor(database, p, &config.Config{}, nil, []Step{newApprovalStep(types.StepReview, `{"findings":[]}`)}, nil)
	executor.SetCapacity(c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	workDir := t.TempDir()
	go func() { done <- executor.Execute(ctx, run, repo, workDir) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	release, err := c.Acquire(ctx, types.StepReview)
	if err != nil {
		t.Fatal("parked review retained capacity:", err)
	}
	release()
	if err := executor.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
