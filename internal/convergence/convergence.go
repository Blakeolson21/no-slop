// Package convergence derives review-loop convergence telemetry from a step's
// persisted execution rounds, and decides when the loop is measurably not
// converging (the "ladder" failure mode: fix rounds that relocate or grow the
// finding set instead of shrinking it).
//
// The package is pure: it reads already-persisted db.StepRound rows plus the
// originally submitted changed-file list and produces a Report. The executor
// evaluates and persists the report once per review round; status surfaces
// only render what was persisted, so there is exactly one evaluation owner
// and no threshold drift between the daemon and the CLI.
//
// The guard is advisory by design: a tripped report never aborts a run. It
// stops the pipeline from spending further automatic fix rounds and parks the
// gate for an explicit decision, with the full history attached, and the
// operator can still respond fix deliberately.
package convergence

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// Report is the per-review-step convergence report persisted as JSON on
// step_results.convergence_json and rendered at the gate.
type Report struct {
	// RoundFindings is the findings count of every review round so far, in
	// round order (e.g. 1,1,1,2,3,3,3 for the observed ladder).
	RoundFindings []int `json:"round_findings"`
	// ReviewMS is the cumulative review execution wall clock across all
	// rounds, excluding time parked at gates.
	ReviewMS int64 `json:"review_ms"`
	// NewSurfaceFindings counts findings (across all rounds) whose file did
	// not appear in the originally submitted diff: surface the fix rounds
	// created rather than repaired. Nil when the submitted file list could
	// not be determined; never a fabricated zero.
	NewSurfaceFindings *int `json:"new_surface_findings,omitempty"`
	// Recurring lists finding classes seen in two or more distinct rounds,
	// keyed by content rather than by file path or generated id, so a defect
	// that moves files between rounds is visible as the same defect.
	Recurring []RecurringClass `json:"recurring,omitempty"`
	// Warning is non-empty exactly when the guard tripped. It names every
	// trigger that fired.
	Warning string `json:"warning,omitempty"`
}

// RecurringClass is one finding class observed in multiple distinct rounds.
type RecurringClass struct {
	Label  string   `json:"label"`
	Rounds []int    `json:"rounds"`
	Files  []string `json:"files,omitempty"`
}

// Tripped reports whether the guard fired for this report.
func (r *Report) Tripped() bool {
	return r != nil && r.Warning != ""
}

// Thresholds configure the guard. A zero value disables that trigger, so the
// zero Thresholds is a fully disabled guard that still produces telemetry.
type Thresholds struct {
	// NonDecreasingRounds trips the guard when the findings count has not
	// decreased across this many trailing consecutive rounds (all non-zero).
	NonDecreasingRounds int
	// RecurringRounds trips the guard when one finding class recurs in this
	// many distinct rounds.
	RecurringRounds int
	// BudgetMS trips the guard when cumulative review execution time reaches
	// this budget.
	BudgetMS int64
}

// BuildReport computes the convergence telemetry for a review step's rounds
// and evaluates the guard against the given thresholds. submittedKnown is
// false when the originally submitted changed-file list could not be
// determined; the new-surface count is then omitted rather than fabricated.
func BuildReport(rounds []*db.StepRound, submittedFiles []string, submittedKnown bool, t Thresholds) Report {
	report := Report{}
	submitted := make(map[string]struct{}, len(submittedFiles))
	for _, f := range submittedFiles {
		submitted[f] = struct{}{}
	}

	newSurface := 0
	var entries []classEntry
	for _, round := range rounds {
		items := roundFindings(round)
		report.RoundFindings = append(report.RoundFindings, len(items))
		report.ReviewMS += round.DurationMS
		for _, f := range items {
			if submittedKnown && f.File != "" {
				if _, ok := submitted[f.File]; !ok {
					newSurface++
				}
			}
			entries = append(entries, classEntry{finding: f, round: round.Round})
		}
	}
	if submittedKnown {
		report.NewSurfaceFindings = &newSurface
	}
	report.Recurring = recurringClasses(entries)
	report.Warning = evaluate(&report, t)
	return report
}

func roundFindings(round *db.StepRound) []types.Finding {
	if round == nil || round.FindingsJSON == nil || *round.FindingsJSON == "" {
		return nil
	}
	parsed, err := types.ParseFindingsJSON(*round.FindingsJSON)
	if err != nil {
		return nil
	}
	return parsed.Items
}

// evaluate returns the guard warning, or "" when no trigger fired.
func evaluate(r *Report, t Thresholds) string {
	var reasons []string
	if reason := nonDecreasingReason(r.RoundFindings, t.NonDecreasingRounds); reason != "" {
		reasons = append(reasons, reason)
	}
	if t.RecurringRounds > 0 {
		for _, class := range r.Recurring {
			if len(class.Rounds) >= t.RecurringRounds {
				reasons = append(reasons, fmt.Sprintf("finding class %q recurred in rounds %s", class.Label, joinInts(class.Rounds, ", ")))
			}
		}
	}
	if t.BudgetMS > 0 && r.ReviewMS >= t.BudgetMS {
		reasons = append(reasons, fmt.Sprintf("review has consumed %s of agent time, at or over the %s budget",
			formatShortDuration(r.ReviewMS), formatShortDuration(t.BudgetMS)))
	}
	if len(reasons) == 0 {
		return ""
	}
	return "review loop is not converging: " + strings.Join(reasons, "; ")
}

func nonDecreasingReason(counts []int, window int) string {
	if window <= 0 || len(counts) < window {
		return ""
	}
	tail := counts[len(counts)-window:]
	for i, c := range tail {
		if c <= 0 {
			return ""
		}
		if i > 0 && c < tail[i-1] {
			return ""
		}
	}
	return fmt.Sprintf("findings per round have not decreased across the last %d rounds (%s)", window, joinInts(tail, ","))
}

// ParseReport decodes a persisted report. It fails soft: any input that does
// not decode to a report with content simply reports absence.
func ParseReport(raw string) (*Report, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var r Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, false
	}
	if len(r.RoundFindings) == 0 && r.Warning == "" && len(r.Recurring) == 0 {
		return nil, false
	}
	return &r, true
}

// FormatRoundCounts renders per-round findings counts as "1,1,1,2,3,3,3".
func FormatRoundCounts(counts []int) string {
	return joinInts(counts, ",")
}

func joinInts(values []int, sep string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, sep)
}

func formatShortDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
