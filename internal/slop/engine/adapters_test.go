package engine_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

type reviewerAgent struct {
	opts agent.RunOpts
}

func TestShellTestRunnerCapturesNonzeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixture")
	}

	result, err := (engine.ShellTestRunner{}).Run(context.Background(), t.TempDir(), "printf 'focused failure'; exit 7")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !strings.Contains(result.Output, "focused failure") {
		t.Fatalf("result = %+v", result)
	}
}

func (a *reviewerAgent) Name() string { return "test" }
func (a *reviewerAgent) Close() error { return nil }
func (a *reviewerAgent) Run(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
	a.opts = opts
	if opts.OnChunk != nil {
		opts.OnChunk("review narrative without newline")
	}
	return &agent.Result{Output: json.RawMessage(`{"findings":[{"lens":"fail-open-default","severity":"error","file":"policy.go","line":12,"description":"unknown becomes allow"}]}`)}, nil
}

func TestAgentReviewerReturnsNamedStructuredFindings(t *testing.T) {
	t.Parallel()

	ag := &reviewerAgent{}
	reviewer := engine.NewAgentReviewer(ag, nil)
	findings, err := reviewer.Review(context.Background(), engine.ReviewRequest{
		WorkDir: "/repo",
		Branch:  "feature/policy",
		BaseRef: "base-sha",
		HeadRef: "head-sha",
		Intent:  "Make policy reads fail closed.",
		Tier:    risk.TierSingleReview,
		Prompt:  "[fail-open-default] inspect permissive defaults",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Lens != "fail-open-default" || findings[0].Line != 12 {
		t.Fatalf("findings = %+v", findings)
	}
	if ag.opts.CWD != "/repo" || len(ag.opts.JSONSchema) == 0 {
		t.Fatalf("agent options = %+v", ag.opts)
	}
	if !strings.Contains(ag.opts.Prompt, "stated intent: Make policy reads fail closed.") {
		t.Fatalf("review prompt did not carry intent:\n%s", ag.opts.Prompt)
	}
	for _, name := range lenses.Names() {
		if !strings.Contains(string(ag.opts.JSONSchema), name) {
			t.Errorf("review schema is missing catalog lens %q", name)
		}
	}
}

func TestAgentReviewerPromptsAdversarialRoundWithPriorFindings(t *testing.T) {
	t.Parallel()

	ag := &reviewerAgent{}
	reviewer := engine.NewAgentReviewer(ag, nil)
	_, err := reviewer.Review(context.Background(), engine.ReviewRequest{
		WorkDir: "/repo",
		Branch:  "feature/policy",
		BaseRef: "base-sha",
		HeadRef: "head-sha",
		Tier:    risk.TierFullAdversarial,
		Round:   engine.ReviewRoundAdversarialChallenge,
		PriorFindings: []engine.Finding{{
			Lens:        "fail-open-default",
			Path:        "policy.go",
			Line:        12,
			Description: "unknown becomes allow",
		}},
		Prompt: "[fail-open-default] inspect permissive defaults",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"adversarial challenge round", "claim to falsify", "unknown becomes allow"} {
		if !strings.Contains(ag.opts.Prompt, want) {
			t.Errorf("adversarial prompt missing %q:\n%s", want, ag.opts.Prompt)
		}
	}
}

func TestAgentReviewerFramesBufferedNarration(t *testing.T) {
	t.Parallel()

	ag := &reviewerAgent{}
	var stream strings.Builder
	reviewer := engine.NewAgentReviewer(ag, func(chunk string) { stream.WriteString(chunk) })
	_, err := reviewer.Review(context.Background(), engine.ReviewRequest{
		WorkDir: "/repo",
		Branch:  "feature/policy",
		BaseRef: "base-sha",
		HeadRef: "head-sha",
		Tier:    risk.TierSingleReview,
		Prompt:  "[fail-open-default] inspect permissive defaults",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "reviewer stream: begin\nreview narrative without newline\nreviewer stream: end\n"
	if stream.String() != want {
		t.Fatalf("reviewer stream = %q, want %q", stream.String(), want)
	}
}
