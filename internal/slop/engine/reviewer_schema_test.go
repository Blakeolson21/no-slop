package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
)

// fixedAgent returns one canned structured response.
type fixedAgent struct{ output string }

func (a fixedAgent) Name() string { return "fixed" }

func (a fixedAgent) Run(context.Context, agent.RunOpts) (*agent.Result, error) {
	return &agent.Result{Output: json.RawMessage(a.output)}, nil
}

func (a fixedAgent) Close() error { return nil }

// TestReviewerResponseWithoutFindingsKeyIsRefused pins the difference between a
// reviewer that found nothing and a reviewer that answered off-schema. Decoding
// into a plain slice made those two identical, which is the wrong default for a
// protocol whose entire output is that object.
func TestReviewerResponseWithoutFindingsKeyIsRefused(t *testing.T) {
	t.Parallel()

	reviewer := engine.NewAgentReviewer(fixedAgent{output: `{"summary":"looks fine"}`}, nil)
	if _, err := reviewer.Review(context.Background(), engine.ReviewRequest{}); err == nil {
		t.Fatal("a response with no findings key was accepted as a clean review")
	} else if !strings.Contains(err.Error(), "findings") {
		t.Fatalf("error = %v, want it to name the missing key", err)
	}
}

// TestEmptyFindingsArrayIsACleanReview is the control: the reviewer really can
// say it found nothing, and that must stay distinguishable from silence.
func TestEmptyFindingsArrayIsACleanReview(t *testing.T) {
	t.Parallel()

	reviewer := engine.NewAgentReviewer(fixedAgent{output: `{"findings":[]}`}, nil)
	findings, err := reviewer.Review(context.Background(), engine.ReviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none", findings)
	}
}

// TestFindingWithoutALensIsRefused keeps a half-filled finding from entering
// the result set as an unattributable one.
func TestFindingWithoutALensIsRefused(t *testing.T) {
	t.Parallel()

	reviewer := engine.NewAgentReviewer(fixedAgent{output: `{"findings":[{"severity":"error","file":"a.go","line":1,"description":"x"}]}`}, nil)
	if _, err := reviewer.Review(context.Background(), engine.ReviewRequest{}); err == nil {
		t.Fatal("a finding naming no lens was accepted")
	}
}
