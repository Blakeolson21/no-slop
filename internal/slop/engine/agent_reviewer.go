package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/slop/lenses"
)

// AgentReviewer adapts an existing no-slop agent to the NoSlop reviewer
// seam.
type AgentReviewer struct {
	ag      agent.Agent
	onChunk func(string)
}

// NewAgentReviewer constructs a structured lens reviewer.
func NewAgentReviewer(ag agent.Agent, onChunk func(string)) *AgentReviewer {
	return &AgentReviewer{ag: ag, onChunk: onChunk}
}

func slopReviewSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"lens":        map[string]any{"type": "string", "enum": lenses.Names()},
						"severity":    map[string]any{"type": "string", "enum": []string{"error", "warning", "info"}},
						"file":        map[string]any{"type": "string"},
						"line":        map[string]any{"type": "integer"},
						"description": map[string]any{"type": "string"},
					},
					"required": []string{"lens", "severity", "file", "line", "description"},
				},
			},
		},
		"required": []string{"findings"},
	}
	encoded, _ := json.Marshal(schema)
	return encoded
}

// Review asks the agent to inspect the repository and return named findings.
func (r *AgentReviewer) Review(ctx context.Context, request ReviewRequest) ([]Finding, error) {
	if r == nil || r.ag == nil {
		return nil, fmt.Errorf("reviewer agent is not configured")
	}
	if request.Round == "" {
		request.Round = ReviewRoundLensReview
	}
	roundTask := `This is the first lens-review round.
- Read the diff and relevant surrounding code.
- Inspect the whole change through every named lens below.`
	if request.Round == ReviewRoundAdversarialChallenge {
		roundTask = `This is the second, adversarial challenge round.
- Treat the first review's conclusion as a claim to falsify, including when it returned no findings.
- Re-read the diff and inspect sibling paths, independent test oracles, permissive error paths, and unsupported assertions the first round may have missed.
- Do not repeat a first-round finding unless you have materially different evidence.`
	}
	prior := ""
	if request.Round == ReviewRoundAdversarialChallenge {
		prior = formatPriorFindings(request.PriorFindings)
	}
	prompt := fmt.Sprintf(`You are NoSlop, the reviewer that knows the author is an AI.

Review the committed change in the repository yourself.

Context:
- branch: %s
- base ref: %s
- head ref: %s
- stated intent: %s
- selected depth: %s
- review round: %s

Task:
%s
- Do not run tests. The full tier owns test execution after this review.
- Report only concrete findings supported by source evidence.
- Return an empty findings array when no lens applies.
%s
%s`, request.Branch, request.BaseRef, request.HeadRef, statedIntent(request.Intent), request.Tier, request.Round, roundTask, prior, request.Prompt)

	var narration strings.Builder
	var capture func(string)
	if r.onChunk != nil {
		capture = func(chunk string) { narration.WriteString(chunk) }
	}
	result, err := r.ag.Run(ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        request.WorkDir,
		JSONSchema: slopReviewSchema(),
		OnChunk:    capture,
		Purpose:    "noslop-review",
	})
	r.flushNarration(narration.String())
	if err != nil {
		return nil, err
	}
	// Findings is a pointer so that an absent key is distinguishable from an
	// empty array. Decoding into a plain slice made a reviewer that returned a
	// schema-violating object read exactly like a reviewer that found nothing,
	// which is the wrong default for a protocol whose entire output is that
	// object.
	var payload struct {
		Findings *[]struct {
			Lens        string `json:"lens"`
			Severity    string `json:"severity"`
			File        string `json:"file"`
			Line        int    `json:"line"`
			Description string `json:"description"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		return nil, fmt.Errorf("parse reviewer findings: %w", err)
	}
	if payload.Findings == nil {
		return nil, fmt.Errorf("parse reviewer findings: response has no %q key, so the review produced no usable verdict", "findings")
	}
	findings := make([]Finding, 0, len(*payload.Findings))
	for index, finding := range *payload.Findings {
		if strings.TrimSpace(finding.Lens) == "" || strings.TrimSpace(finding.Description) == "" {
			return nil, fmt.Errorf("parse reviewer findings: finding %d names no lens or carries no description", index)
		}
		findings = append(findings, Finding{
			Lens:        finding.Lens,
			Severity:    reviewerSeverity(finding.Severity),
			Path:        finding.File,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return findings, nil
}

// reviewerSeverities are the only severities a reviewer may assert.
//
// SeverityNotice is the one value that makes a finding non-blocking, and it is
// reserved for the single case the engine raises itself. A reviewer's severity
// arrives in a model response whose input is the diff under review, which is
// this gate's stated threat model, so copying it verbatim put the verdict in
// reach of one word: a real finding returned as "notice" printed under the
// findings list and still reached `verdict: pass` at exit 0. The JSON schema
// already excludes it, but schema conformance is the backend's best effort
// rather than this package's guarantee, so nothing downstream may assume it
// held. Anything unrecognised, including an absent severity, becomes blocking.
var reviewerSeverities = map[string]struct{}{
	SeverityError: {},
	"warning":     {},
	"info":        {},
}

func reviewerSeverity(reported string) string {
	normalized := strings.ToLower(strings.TrimSpace(reported))
	if _, ok := reviewerSeverities[normalized]; ok {
		return normalized
	}
	return SeverityError
}

func statedIntent(intent string) string {
	if strings.TrimSpace(intent) == "" {
		return "not supplied"
	}
	return strings.TrimSpace(intent)
}

func (r *AgentReviewer) flushNarration(narration string) {
	if r.onChunk == nil || narration == "" {
		return
	}
	r.onChunk("reviewer stream: begin\n")
	r.onChunk(narration)
	if !strings.HasSuffix(narration, "\n") {
		r.onChunk("\n")
	}
	r.onChunk("reviewer stream: end\n")
}

func formatPriorFindings(findings []Finding) string {
	if len(findings) == 0 {
		return "First-round findings: none."
	}
	var summary strings.Builder
	summary.WriteString("First-round findings to challenge:\n")
	for _, finding := range findings {
		fmt.Fprintf(&summary, "- [%s] %s:%d: %s\n", finding.Lens, finding.Path, finding.Line, finding.Description)
	}
	return strings.TrimRight(summary.String(), "\n")
}
