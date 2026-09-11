package pipeline

import (
	"context"
	"sync"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// Capacity is shared by every executor in a host daemon. Review and suite
// execution occupy independent pools; approval waits occupy neither. Release
// closes a notification channel, so admission never depends on a polling timer.
type Capacity struct {
	mu      sync.Mutex
	limits  config.Concurrency
	active  [2]int
	changed chan struct{}
}

func NewCapacity(limits config.Concurrency) *Capacity {
	c := &Capacity{changed: make(chan struct{})}
	c.Configure(limits)
	return c
}

// Configure applies the operator config on run admission, including recovery.
// Lowering a cap drains existing holders; it never interrupts their work.
func (c *Capacity) Configure(limits config.Concurrency) {
	defaults := config.DefaultConcurrency()
	if limits.Reviews <= 0 {
		limits.Reviews = defaults.Reviews
	}
	if limits.Suites <= 0 {
		limits.Suites = defaults.Suites
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limits = limits
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Capacity) Acquire(ctx context.Context, step types.StepName) (func(), error) {
	if c == nil {
		return func() {}, context.Cause(ctx)
	}
	pool := 0
	switch step {
	case types.StepReview:
	case types.StepTest, types.StepLint:
		pool = 1
	default:
		return func() {}, context.Cause(ctx)
	}
	for {
		c.mu.Lock()
		if err := context.Cause(ctx); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		limit := c.limits.Reviews
		if pool == 1 {
			limit = c.limits.Suites
		}
		if c.active[pool] < limit {
			c.active[pool]++
			c.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					c.mu.Lock()
					defer c.mu.Unlock()
					c.active[pool]--
					close(c.changed)
					c.changed = make(chan struct{})
				})
			}, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-changed:
		}
	}
}

func (e *Executor) SetCapacity(c *Capacity) { e.capacity = c }

func (e *Executor) executeWithCapacity(step Step, sctx *StepContext) (*StepOutcome, error) {
	duty := step.Name()
	if duty == types.StepDocument && sctx.Config != nil && sctx.Config.Commands.Lint == "" {
		duty = types.StepLint
	}
	release, err := e.capacity.Acquire(sctx.Ctx, duty)
	if err != nil {
		return nil, err
	}
	defer release()
	return step.Execute(sctx)
}
