package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
)

func TestFallbackFailsClosedOnAQuotaOutage(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 44, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }

	codex := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New(codexQuotaStderr)
	}}
	claude := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(codex, store, clock),
		WithLaneHealth(claude, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{})
	if err == nil {
		t.Fatalf("expected quota outage to fail closed")
	}
	if codex.calls != 1 || claude.calls != 0 {
		t.Fatalf("calls: codex=%d claude=%d, want 1/0", codex.calls, claude.calls)
	}
	if !IsQuotaOutage(err) {
		t.Fatalf("error %v must classify as quota outage", err)
	}
}

func TestFallbackDoesNotSubstituteAReservedLaneAfterQuotaOutage(t *testing.T) {
	now := time.Date(2026, 8, 21, 20, 5, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }

	codex := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New(codexQuotaStderr)
	}}
	play := &fallbackTestAgent{name: "play", run: func() (*Result, error) {
		return &Result{Text: "reserved seat burned"}, nil
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(codex, store, clock),
		WithLaneHealth(play, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{})
	if err == nil {
		t.Fatalf("expected quota outage to fail closed")
	}
	if play.calls != 0 {
		t.Fatalf("reserved play lane was invoked %d times, want 0", play.calls)
	}
	if !strings.Contains(err.Error(), "codex") || !strings.Contains(err.Error(), "quota-exhausted") {
		t.Fatalf("error %q must clearly report the quota refusal", err)
	}
}

func TestFallbackFailsWithEveryLaneResetTimeWhenAllLanesAreExhausted(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 44, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }

	claude := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return nil, errors.New("claude exited: exit status 1: You've hit your session limit · resets 9:15 PM")
	}}
	codex := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New(codexQuotaStderr)
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(claude, store, clock),
		WithLaneHealth(codex, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{})
	if err == nil {
		t.Fatalf("expected the run to fail when every lane is exhausted")
	}
	msg := err.Error()
	for _, want := range []string{
		"claude",
		time.Date(2026, 8, 4, 21, 15, 0, 0, time.Local).Format("2006-01-02 15:04 MST"),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("all-lanes-exhausted error %q must contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "codex") {
		t.Fatalf("all-lanes-exhausted error %q must not name an untried fallback lane", msg)
	}
	if codex.calls != 0 {
		t.Fatalf("codex calls = %d, want 0", codex.calls)
	}
	var outageErr *LaneOutageError
	if errors.As(err, &outageErr) {
		t.Fatalf("the aggregate must not masquerade as a single-lane outage: %v", err)
	}
	// Telemetry still needs to know the step died on quota, so the aggregate
	// classifies as a quota outage without being a single recoverable lane.
	if !IsQuotaOutage(err) {
		t.Fatalf("the aggregate must classify as a quota outage: %v", err)
	}
}

// TestIsQuotaOutage pins the classifier's boundary: both lane-outage shapes
// are quota, an ordinary provider exit whose text resembles a banner is not.
func TestIsQuotaOutage(t *testing.T) {
	until := time.Date(2026, 8, 7, 23, 6, 0, 0, time.Local)
	if !IsQuotaOutage(&LaneOutageError{Lane: "codex", Until: until}) {
		t.Fatal("a skipped marked lane must classify as a quota outage")
	}
	if !IsQuotaOutage(&LaneOutageError{Lane: "codex", Until: until, cause: errors.New("codex exited: exit status 1")}) {
		t.Fatal("a freshly classified banner must classify as a quota outage")
	}
	if IsQuotaOutage(errors.New("codex exited: exit status 1: network unreachable")) {
		t.Fatal("an ordinary exit must not classify as a quota outage")
	}
}

// A session-scoped invocation is narrowed to the one lane that owns the
// session, so its failure must not claim every configured lane is out: that
// sentence is what gets logged as the resume-failure reason, and the other
// lanes were never tried.
func TestFallbackDoesNotClaimEveryLaneWhenOnlyTheSessionLaneWasTried(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 44, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }

	codex := &fallbackTestAgent{name: "codex", resumable: true, run: func() (*Result, error) {
		return nil, errors.New(codexQuotaStderr)
	}}
	claude := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(codex, store, clock),
		WithLaneHealth(claude, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{
		Session: &SessionRef{ID: "thread-1", Agent: "codex"},
	})
	if err == nil {
		t.Fatalf("expected the session-scoped resume to fail")
	}
	if claude.calls != 0 {
		t.Fatalf("claude calls = %d, want 0: a session-scoped run must not use another lane", claude.calls)
	}
	msg := err.Error()
	if !strings.Contains(msg, "codex") {
		t.Fatalf("message %q must name the lane that was actually tried", msg)
	}
	if strings.Contains(msg, "every configured agent lane") {
		t.Fatalf("message %q must not claim lanes it never tried were exhausted", msg)
	}
}

func TestFallbackStopsBeforeANonQuotaFallbackAfterQuotaOutage(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 44, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }

	codex := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New(codexQuotaStderr)
	}}
	claude := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return nil, errors.New("claude exited: exit status 2: parse failure")
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(codex, store, clock),
		WithLaneHealth(claude, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{})
	if err == nil {
		t.Fatalf("expected a failure")
	}
	if strings.Contains(err.Error(), "parse failure") {
		t.Fatalf("error %q must not come from a substituted fallback lane", err)
	}
	if claude.calls != 0 {
		t.Fatalf("claude calls = %d, want 0", claude.calls)
	}
}

// Marks recorded by an earlier process are honored on the very first invocation
// of a fresh chain, so a daemon restart does not restart the rediscovery cost.
func TestFallbackHonorsMarksWrittenByAnEarlierProcess(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 44, 0, 0, time.Local)
	store := laneTestStore(t, &now)
	clock := func() time.Time { return now }
	if err := store.Mark(lanehealth.Outage{
		Lane:   "codex",
		Until:  now.Add(3 * 24 * time.Hour),
		Reason: "You've hit your usage limit",
	}); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	codex := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return &Result{Text: "should not run"}, nil
	}}
	claude := &fallbackTestAgent{name: "claude", run: func() (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	chain := NewFallback([]Agent{
		WithLaneHealth(codex, store, clock),
		WithLaneHealth(claude, store, clock),
	})

	_, err := chain.Run(context.Background(), RunOpts{})
	if err == nil {
		t.Fatalf("expected the persisted mark to fail closed")
	}
	if claude.calls != 0 {
		t.Fatalf("claude calls = %d, want 0", claude.calls)
	}
	if codex.calls != 0 {
		t.Fatalf("codex calls = %d, want 0", codex.calls)
	}
}
