package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

// AgentReviewer adapts an existing no-mistakes agent to the NoSlop reviewer
// seam.
type AgentReviewer struct {
	ag      agent.Agent
	onChunk func(string)
}

// NewAgentReviewer constructs a structured lens reviewer.
func NewAgentReviewer(ag agent.Agent, onChunk func(string)) *AgentReviewer {
	return &AgentReviewer{ag: ag, onChunk: onChunk}
}

var slopReviewSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "lens": {"type": "string", "enum": ["vacuous-check", "test-capitulation", "self-consistent-oracle", "comment-defended-workaround", "scope-expansion", "asserted-followup-without-artifact", "fail-open-default", "rule-applied-in-one-place-not-sibling"]},
          "severity": {"type": "string", "enum": ["error", "warning", "info"]},
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "description": {"type": "string"}
        },
        "required": ["lens", "severity", "file", "line", "description"]
      }
    }
  },
  "required": ["findings"]
}`)

// Review asks the agent to inspect the repository and return named findings.
func (r *AgentReviewer) Review(ctx context.Context, request ReviewRequest) ([]Finding, error) {
	if r == nil || r.ag == nil {
		return nil, fmt.Errorf("reviewer agent is not configured")
	}
	prompt := fmt.Sprintf(`You are NoSlop, the reviewer that knows the author is an AI.

Review the committed change in the repository yourself.

Context:
- branch: %s
- base ref: %s
- head ref: %s
- selected depth: %s

Task:
- Read the diff and relevant surrounding code.
- Inspect the whole change through every named lens below.
- Do not run tests. The full tier owns test execution after this review.
- Report only concrete findings supported by source evidence.
- Return an empty findings array when no lens applies.
%s`, request.Branch, request.BaseRef, request.HeadRef, request.Tier, request.Prompt)

	result, err := r.ag.Run(ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        request.WorkDir,
		JSONSchema: slopReviewSchema,
		OnChunk:    r.onChunk,
		Purpose:    "noslop-review",
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Findings []struct {
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
	findings := make([]Finding, 0, len(payload.Findings))
	for _, finding := range payload.Findings {
		findings = append(findings, Finding{
			Lens:        finding.Lens,
			Severity:    finding.Severity,
			Path:        finding.File,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return findings, nil
}
