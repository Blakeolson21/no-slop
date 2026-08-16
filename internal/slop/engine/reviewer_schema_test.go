package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
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

// TestReviewerCannotAssertANonBlockingSeverity pins the one place an
// externally supplied severity reaches Finding.Blocks. engine.SeverityNotice is
// reserved for the submodule case the engine raises itself, so a reviewer
// claiming it put the whole verdict in reach of a single word: the finding
// printed under the findings list and the run still reached verdict pass at
// exit 0. The reviewer's response is shaped by the diff under review, which is
// exactly the input this gate does not trust.
func TestReviewerCannotAssertANonBlockingSeverity(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name     string
		severity string
		want     string
	}{
		{"the reserved notice severity", "notice", engine.SeverityError},
		{"a severity nobody defined", "cosmetic", engine.SeverityError},
		{"an absent severity", "", engine.SeverityError},
		{"an error stays an error", "error", engine.SeverityError},
		{"a warning survives verbatim", "warning", "warning"},
		{"an info survives verbatim", "info", "info"},
	} {
		response := `{"findings":[{"lens":"fabricated-authority","severity":"` + probe.severity + `","file":"auth.go","line":10,"description":"guard removed"}]}`
		findings, err := engine.NewAgentReviewer(fixedAgent{output: response}, nil).Review(context.Background(), engine.ReviewRequest{})
		if err != nil {
			t.Fatalf("%s: %v", probe.name, err)
		}
		if len(findings) != 1 {
			t.Fatalf("%s: findings = %+v, want the finding kept", probe.name, findings)
		}
		if findings[0].Severity != probe.want {
			t.Errorf("%s: severity = %q, want %q", probe.name, findings[0].Severity, probe.want)
		}
		if !findings[0].Blocks() {
			t.Errorf("%s: a reviewer finding did not block the verdict", probe.name)
		}
	}
}
