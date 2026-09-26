package convergence

import (
	"fmt"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// Only outstanding actionable findings can require another fix. A clean or
// no-op-only final round can finish even when it uses the last allowed round.
func actionable(f types.Finding) bool {
	return f.Action == types.ActionAutoFix || f.Action == types.ActionAskUser
}

func stopReason(rounds []*db.StepRound, t Thresholds) string {
	if len(rounds) == 0 {
		return ""
	}
	pending := false
	for _, f := range roundFindings(rounds[len(rounds)-1]) {
		pending = pending || actionable(f)
	}
	if !pending {
		return ""
	}
	var reasons []string
	if t.MaxRounds > 0 && len(rounds) >= t.MaxRounds {
		reasons = append(reasons, fmt.Sprintf("review reached the %d-round limit with actionable findings", t.MaxRounds))
	}
	if t.MaxRecurringRounds == 1 {
		reasons = append(reasons, "actionable finding class reached the 1-round recurrence limit")
	} else if t.MaxRecurringRounds > 1 {
		var entries []classEntry
		for _, r := range rounds {
			for _, f := range roundFindings(r) {
				if actionable(f) {
					entries = append(entries, classEntry{finding: f, round: r.Round})
				}
			}
		}
		for _, class := range recurringClasses(entries) {
			if len(class.Rounds) >= t.MaxRecurringRounds {
				reasons = append(reasons, fmt.Sprintf("finding class %q recurred in %d rounds (limit %d)", class.Label, len(class.Rounds), t.MaxRecurringRounds))
			}
		}
	}
	return strings.Join(reasons, "; ")
}

// Keep the full round history, including previously resolved findings, as
// acceptance seeds. This is a draft for a ticket, never an automatic filing.
func redesignTicket(rounds []*db.StepRound, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review loop requires redesign\n\n%s\n\n## Acceptance seeds\n\nHistorical findings below may already be resolved; the final round lists the findings at termination.\n", reason)
	for _, r := range rounds {
		fmt.Fprintf(&b, "\n### Round %d\n", r.Round)
		for _, f := range roundFindings(r) {
			location := f.File
			if f.Line > 0 {
				location += fmt.Sprintf(":%d", f.Line)
			}
			fmt.Fprintf(&b, "- [ ] [%s; %s] %s (%s): %s\n", f.Severity, f.Action, location, f.ID, f.Description)
		}
	}
	return safeurl.RedactText(b.String())
}
