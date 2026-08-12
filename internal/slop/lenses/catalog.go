// Package lenses owns the named AI-authorship review taxonomy.
package lenses

import (
	"fmt"
	"strings"
)

// Lens is one failure mode supplied to the reviewer.
type Lens struct {
	Name              string
	Description       string
	DetectionGuidance string
	MechanicalCheck   string
}

// Catalog returns the stable v1 lens catalog in prompt order.
func Catalog() []Lens {
	return []Lens{
		{
			Name:              "vacuous-check",
			Description:       "A check looks protective but cannot disagree with the value it validates.",
			DetectionGuidance: "Trace both sides of assertions and guards to independent sources. Flag the same expression on both sides, comparison helpers given identical arguments, and a previous/before snapshot assigned only post-mutation before being compared.",
			MechanicalCheck:   "same-expression-and-post-mutation-snapshot",
		},
		{
			Name:              "test-capitulation",
			Description:       "Tests were changed to accept the implementation instead of preserving the required behavior.",
			DetectionGuidance: "Compare test strength with the base revision. Look for deleted cases, skipped tests, a numeric tolerance changed to a larger threshold, weaker assertions, changed expected values without external evidence, and a lower test count even when the suite passes.",
			MechanicalCheck:   "test-count-and-tolerance",
		},
		{
			Name:              "self-consistent-oracle",
			Description:       "A test oracle repeats the implementation and can only confirm the same mistake twice.",
			DetectionGuidance: "Check whether an independent literal, standard vector, formula, fixture, or snapshot was replaced by the production helper used to compute the actual result. Require truth from a specification, known example, independent implementation, or worked literal.",
			MechanicalCheck:   "literal-oracle-replaced-by-computation",
		},
		{
			Name:              "comment-defended-workaround",
			Description:       "A long explanatory comment is used to legitimize a workaround instead of resolving its design cost.",
			DetectionGuidance: "Treat comments that justify bypasses, duplication, special cases, or disabled checks as design signals. Pair the comment with the nearby permissive return or security bypass; verify the workaround is necessary, bounded, and owned rather than accepting the comment as proof.",
			MechanicalCheck:   "justification-comment-plus-permissive-action",
		},
		{
			Name:              "scope-expansion",
			Description:       "A fix quietly adds behavior or infrastructure beyond the requested change.",
			DetectionGuidance: "Compare every new file and subsystem with the stated intent and original failing path. Flag new features, generalized frameworks, unrelated cleanup, schema work, or enforcement mechanisms outside the smallest correct fix.",
			MechanicalCheck:   "intent-to-new-file-scope",
		},
		{
			Name:              "asserted-followup-without-artifact",
			Description:       "The change claims a follow-up exists without an inspectable artifact.",
			DetectionGuidance: "Verify claims such as filed, tracked, assigned, approved, scheduled, or deferred have a durable URL, issue number, ticket ID, or approval reference in the available artifacts. A prose promise or comment is not evidence of a follow-up.",
			MechanicalCheck:   "assertion-without-durable-reference",
		},
		{
			Name:              "fail-open-default",
			Description:       "An unknown, failed, or unparsed state becomes permission to continue.",
			DetectionGuidance: "Follow error, empty, timeout, parse-failure, and default branches. Flag nil, nil, true, allow, healthy, empty findings, or a privileged object returned when the state could not be determined, unless explicit policy proves that result safe.",
			MechanicalCheck:   "error-state-to-permissive-result",
		},
		{
			Name:              "rule-applied-in-one-place-not-sibling",
			Description:       "A rule fixes one path while equivalent sibling paths remain exposed.",
			DetectionGuidance: "Find sibling adapters, handlers, versioned routes, formats, platforms, transports, and state transitions that enforce the same invariant. Compare strict and permissive branches explicitly; verify the rule lives at the earliest shared owner or is deliberately and completely repeated.",
			MechanicalCheck:   "contradictory-sibling-branches",
		},
	}
}

// Names returns the stable lens names for structured reviewer output.
func Names() []string {
	catalog := Catalog()
	names := make([]string, 0, len(catalog))
	for _, lens := range catalog {
		names = append(names, lens.Name)
	}
	return names
}

// ReviewerPrompt renders the taxonomy as explicit finding labels and checks.
func ReviewerPrompt() string {
	return ReviewerPromptWithPriority(nil)
}

// ReviewerPromptWithPriority moves history-selected lenses to the front while
// preserving the complete catalog exactly once.
func ReviewerPromptWithPriority(priority []string) string {
	var prompt strings.Builder
	prompt.WriteString("\n\nAI-authorship slop lenses:\n")
	prompt.WriteString("Review the entire change through every lens below. Prefix a finding description with the matching [lens-name]. Do not emit a lens finding without source evidence.\n")
	for _, lens := range catalogWithPriority(priority) {
		fmt.Fprintf(&prompt, "- [%s] %s Detection: %s", lens.Name, lens.Description, lens.DetectionGuidance)
		if lens.MechanicalCheck != "" {
			fmt.Fprintf(&prompt, " Mechanical pre-check: %s.", lens.MechanicalCheck)
		}
		prompt.WriteByte('\n')
	}
	return strings.TrimRight(prompt.String(), "\n")
}

func catalogWithPriority(priority []string) []Lens {
	catalog := Catalog()
	byName := make(map[string]Lens, len(catalog))
	for _, lens := range catalog {
		byName[lens.Name] = lens
	}
	ordered := make([]Lens, 0, len(catalog))
	seen := make(map[string]bool, len(catalog))
	for _, name := range priority {
		if lens, ok := byName[name]; ok && !seen[name] {
			ordered = append(ordered, lens)
			seen[name] = true
		}
	}
	for _, lens := range catalog {
		if !seen[lens.Name] {
			ordered = append(ordered, lens)
		}
	}
	return ordered
}
