package tui

// Compatibility test for the parked non-converging state as the TUI shows it.
//
// The convergence guard is advisory by design: when the review loop trips the
// guard, the review step parks at its gate (fix_review) with the tripped
// report persisted on it, and the run stays non-terminal awaiting the agent's
// decision. No distinct terminal state exists, and the TUI must not invent
// one: the parked review renders as a paused gate awaiting a decision, never
// as completed, failed, or cancelled.

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func parkedNonconvergingRun() *ipc.RunInfo {
	run := testRun()
	run.Steps[0].Status = types.StepStatusFixReview
	tripped := `{"round_findings":[1,1,1,2,3,3,3],"review_ms":11520000,"warning":"review loop is not converging: findings per round have not decreased across the last 3 rounds (3,3,3)"}`
	run.Steps[0].ConvergenceJSON = &tripped
	return run
}

func TestParkedNonconvergingReviewRendersAsPausedGate(t *testing.T) {
	run := parkedNonconvergingRun()
	m := NewModel("", nil, run)
	m.width = 80
	m.height = 50

	// The header shows the review parked at its gate, not a terminal state.
	if title := m.terminalTitle(); title != "⏸ Review - feature/foo" {
		t.Fatalf("parked nonconverging review must render as a paused gate in the title, got %q", title)
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "⏸ Review") {
		t.Errorf("parked nonconverging review must render with the paused-gate indicator:\n%s", view)
	}
	if banner := renderOutcomeBanner(run, run.Steps); banner != "" {
		t.Errorf("a run parked by the convergence guard must not render a terminal outcome banner: %q", banner)
	}
}
