package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fallbackTestAgent struct {
	name      string
	run       func() (*Result, error)
	calls     int
	resumable bool
}

func (a *fallbackTestAgent) Name() string { return a.name }

func (a *fallbackTestAgent) Run(context.Context, RunOpts) (*Result, error) {
	a.calls++
	return a.run()
}

func (a *fallbackTestAgent) Close() error { return nil }

func (a *fallbackTestAgent) SupportsSessionResume() bool { return a.resumable }

func TestFallbackAgentFallsBackOnLaunchFailure(t *testing.T) {
	first := &fallbackTestAgent{
		name: "codex",
		run: func() (*Result, error) {
			return nil, errors.New(`codex start: exec: "codex": executable file not found`)
		},
	}
	second := &fallbackTestAgent{
		name: "claude",
		run: func() (*Result, error) {
			return &Result{Text: "ok"}, nil
		},
	}
	var chunks []string

	result, err := NewFallback([]Agent{first, second}).Run(context.Background(), RunOpts{
		OnChunk: func(text string) { chunks = append(chunks, text) },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("Run() result = %+v, want text ok", result)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = first %d second %d, want 1/1", first.calls, second.calls)
	}
	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, "agent codex failed") || !strings.Contains(joined, "falling back to claude") {
		t.Fatalf("fallback log missing, got %q", joined)
	}
}

func TestFallbackAgentDoesNotFallBackOnFindingsResult(t *testing.T) {
	first := &fallbackTestAgent{
		name: "codex",
		run: func() (*Result, error) {
			return &Result{Output: []byte(`{"findings":[{"severity":"warning","description":"issue"}],"summary":"1 issue"}`)}, nil
		},
	}
	second := &fallbackTestAgent{
		name: "claude",
		run: func() (*Result, error) {
			return &Result{Text: "should not run"}, nil
		},
	}

	result, err := NewFallback([]Agent{first, second}).Run(context.Background(), RunOpts{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(result.Output) == "" {
		t.Fatalf("Run() result = %+v, want findings output", result)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("calls = first %d second %d, want 1/0", first.calls, second.calls)
	}
}

func TestFallbackAgentDoesNotFallBackOnStructuredOutputError(t *testing.T) {
	parseErr := errors.New(`codex output parse: invalid JSON (output snippet: "not json")`)
	first := &fallbackTestAgent{
		name: "codex",
		run: func() (*Result, error) {
			return nil, parseErr
		},
	}
	second := &fallbackTestAgent{
		name: "claude",
		run: func() (*Result, error) {
			return &Result{Text: "should not run"}, nil
		},
	}

	_, err := NewFallback([]Agent{first, second}).Run(context.Background(), RunOpts{})
	if !errors.Is(err, parseErr) {
		t.Fatalf("Run() error = %v, want %v", err, parseErr)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("calls = first %d second %d, want 1/0", first.calls, second.calls)
	}
}

func TestIsAgentUnavailableClassifiesNarratorDeathsAcrossAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "non-claude empty exit message", err: errors.New("acpx exited: exit status 1: "), want: true},
		{
			name: "structurally marked quota lane",
			err: &LaneOutageError{
				Lane:   "codex",
				Until:  time.Date(2026, 9, 3, 4, 0, 0, 0, time.Local),
				Reason: "You've hit your usage limit",
			},
			want: true,
		},
		{
			name: "aggregate quota outage",
			err:  allLanesExhausted([]*LaneOutageError{{Lane: "codex", Until: time.Date(2026, 9, 3, 4, 0, 0, 0, time.Local)}}, true),
			want: true,
		},
		{name: "transient http 429 in provider text", err: errors.New("provider request failed with HTTP 429: rate limited"), want: true},
		{name: "timeout", err: context.DeadlineExceeded, want: true},
		{name: "test failure is not an agent death", err: errors.New("tests failed with exit code 1"), want: false},
		{name: "quota-shaped prose without a process death is not an agent death", err: errors.New("the change removes the usage limit banner"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAgentUnavailable(tc.err); got != tc.want {
				t.Fatalf("IsAgentUnavailable(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Timeout and transient-network errors now route to the next lane, so the
// chain must still stop when the invoking context itself died: the next lane
// would inherit the same dead context and bury the cancellation.
func TestFallbackAgentStopsAtCancellationInsteadOfSpendingTheNextLane(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		cancel()
		return nil, fmt.Errorf("codex exited: %w", context.Canceled)
	}}
	second := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}

	result, err := NewFallback([]Agent{first, second}).Run(ctx, RunOpts{})
	if err == nil {
		t.Fatalf("cancelled run returned result %+v, want the cancellation error", result)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want it to carry context.Canceled", err)
	}
	if second.calls != 0 {
		t.Fatalf("claude was invoked %d times, want 0 after cancellation", second.calls)
	}
}

func TestFallbackAgent_ForwardsSessionCapability(t *testing.T) {
	first := &fallbackTestAgent{name: "codex", resumable: true, run: func() (*Result, error) { return &Result{}, nil }}
	second := &fallbackTestAgent{name: "claude", resumable: true, run: func() (*Result, error) { return &Result{}, nil }}
	if !SupportsSessionResume(NewFallback([]Agent{WithSteering(first, "/evidence"), WithSteering(second, "/evidence")})) {
		t.Fatal("fallback's primary resumable agent must retain session support")
	}
}

func TestFallbackAgent_ReportsEveryAttempt(t *testing.T) {
	first := &fallbackTestAgent{
		name: "codex",
		run: func() (*Result, error) {
			return nil, errors.New(`codex start: executable not found`)
		},
	}
	second := &fallbackTestAgent{
		name: "claude",
		run: func() (*Result, error) {
			return &Result{Text: "ok"}, nil
		},
	}
	var attempts []Attempt
	_, err := NewFallback([]Agent{first, second}).Run(context.Background(), RunOpts{
		OnAttempt: func(attempt Attempt) { attempts = append(attempts, attempt) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Agent != "codex" || attempts[0].Err == nil {
		t.Fatalf("first attempt = %+v", attempts[0])
	}
	if attempts[1].Agent != "claude" || attempts[1].Result == nil || attempts[1].Result.Text != "ok" {
		t.Fatalf("second attempt = %+v", attempts[1])
	}
}
