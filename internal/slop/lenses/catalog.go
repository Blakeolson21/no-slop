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
			DetectionGuidance: "Trace both sides of assertions and guards to independent sources. Flag self-comparisons, copies derived from the same object, and predicates made unreachable by an earlier assignment.",
		},
		{
			Name:              "test-capitulation",
			Description:       "Tests were changed to accept the implementation instead of preserving the required behavior.",
			DetectionGuidance: "Compare test strength with the base revision. Look for deleted cases, skipped tests, broader tolerances, weaker assertions, changed expected values without external evidence, and a lower test count even when the suite passes.",
			MechanicalCheck:   "test-count-floor",
		},
		{
			Name:              "self-consistent-oracle",
			Description:       "A test oracle repeats the implementation and can only confirm the same mistake twice.",
			DetectionGuidance: "Check whether expected vectors, formulas, fixtures, or snapshots were copied from the production algorithm. Require expected results from a specification, known example, independent implementation, or worked literal.",
		},
		{
			Name:              "comment-defended-workaround",
			Description:       "A long explanatory comment is used to legitimize a workaround instead of resolving its design cost.",
			DetectionGuidance: "Treat comments that justify bypasses, duplication, special cases, or disabled checks as design signals. Verify the workaround is necessary, bounded, and owned rather than accepting the comment as proof.",
		},
		{
			Name:              "scope-expansion",
			Description:       "A fix quietly adds behavior or infrastructure beyond the requested change.",
			DetectionGuidance: "Compare the diff with the stated intent and original failing path. Flag new features, generalized frameworks, unrelated cleanup, or enforcement mechanisms that were not required for the smallest correct fix.",
		},
		{
			Name:              "asserted-followup-without-artifact",
			Description:       "The change claims a follow-up exists without an inspectable artifact.",
			DetectionGuidance: "Verify claims such as filed, tracked, scheduled, or deferred have a durable reference in the available artifacts. A prose promise or comment is not evidence of a follow-up.",
		},
		{
			Name:              "fail-open-default",
			Description:       "An unknown, failed, or unparsed state becomes permission to continue.",
			DetectionGuidance: "Follow error, empty, timeout, parse-failure, and default branches. Flag any path where could-not-determine is converted into allow, pass, ready, empty findings, or another permissive result without explicit policy.",
		},
		{
			Name:              "rule-applied-in-one-place-not-sibling",
			Description:       "A rule fixes one path while equivalent sibling paths remain exposed.",
			DetectionGuidance: "Find sibling adapters, handlers, formats, platforms, and state transitions that enforce the same invariant. Verify the rule lives at the earliest shared owner or is deliberately and completely repeated.",
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
	var prompt strings.Builder
	prompt.WriteString("\n\nAI-authorship slop lenses:\n")
	prompt.WriteString("Review the entire change through every lens below. Prefix a finding description with the matching [lens-name]. Do not emit a lens finding without source evidence.\n")
	for _, lens := range Catalog() {
		fmt.Fprintf(&prompt, "- [%s] %s Detection: %s", lens.Name, lens.Description, lens.DetectionGuidance)
		if lens.MechanicalCheck != "" {
			fmt.Fprintf(&prompt, " Mechanical pre-check: %s.", lens.MechanicalCheck)
		}
		prompt.WriteByte('\n')
	}
	return strings.TrimRight(prompt.String(), "\n")
}
