package cli

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func trippedConvergenceJSON() string {
	return `{
		"round_findings":[1,1,1,2,3,3,3],
		"review_ms":11520000,
		"new_surface_findings":4,
		"recurring":[{"label":"env-parse-semantic","rounds":[4,6,7],"files":["restore.sh","backup.sh","convex-env.sh"]}],
		"warning":"review loop is not converging: findings per round have not decreased across the last 3 rounds (3,3,3)"
	}`
}

// A gate return carries the full per-round convergence history so a
// non-converging loop is visible without manually tallying rounds across
// separate tool invocations.
func TestGateRendersConvergenceHistory(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "fix_review",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r7-1", Severity: "error", File: "convex-env.sh", Action: types.ActionAskUser, Description: "env parsing diverges"},
		}, "3 findings"),
		ConvergenceJSON: trippedConvergenceJSON(),
	}
	out := axiDoc(gateFields(gate)...)

	for _, want := range []string{
		"convergence:",
		"rounds:",
		"1,1,1,2,3,3,3",
		"review_time: 3h12m",
		"findings_outside_submitted_diff: 4",
		"env-parse-semantic (rounds 4,6,7)",
		"review loop is not converging",
		// The guard demotes fix from being the obvious next step.
		"Do not respond `fix` by default",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("gate missing %q in:\n%s", want, out)
		}
	}
	// The warning guidance leads the help block: it must come before the
	// standard fix instruction.
	warnIdx := strings.Index(out, "Do not respond `fix` by default")
	fixIdx := strings.Index(out, "--action fix --findings")
	if warnIdx == -1 || fixIdx == -1 || warnIdx > fixIdx {
		t.Errorf("convergence guidance should precede the fix instruction (warn=%d fix=%d):\n%s", warnIdx, fixIdx, out)
	}
}

// A healthy report renders history without the warning or the demotion note.
func TestGateRendersHealthyConvergenceWithoutWarning(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "awaiting_approval",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r1-1", Severity: "warning", File: "a.go", Action: types.ActionAskUser, Description: "x"},
		}, "1 finding"),
		ConvergenceJSON: `{"round_findings":[3,1],"review_ms":60000}`,
	}
	out := axiDoc(gateFields(gate)...)
	if !strings.Contains(out, "rounds:") || !strings.Contains(out, "3,1") {
		t.Errorf("healthy gate should still carry round history:\n%s", out)
	}
	if strings.Contains(out, "warning:") || strings.Contains(out, "Do not respond `fix` by default") {
		t.Errorf("healthy gate must not carry the convergence warning:\n%s", out)
	}
}

// Gates without a persisted report (legacy runs, non-review steps) render
// exactly as before.
func TestGateWithoutConvergenceReportUnchanged(t *testing.T) {
	gate := stepView{
		Name:   "review",
		Status: "awaiting_approval",
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "review-1", Severity: "warning", File: "main.go", Action: types.ActionAskUser, Description: "x"},
		}, "1 finding"),
	}
	out := axiDoc(gateFields(gate)...)
	if strings.Contains(out, "convergence:") {
		t.Errorf("gate without a report must not render a convergence block:\n%s", out)
	}
}

// --yes must stop auto-funding fix rounds the moment the guard trips: a
// non-converging gate is handed back as an explicit decision point.
func TestGateResolution_ConvergenceTrippedParksInsteadOfFix(t *testing.T) {
	actionable := findingsJSON(t, []types.Finding{
		{ID: "r3-1", Severity: "warning", File: "a.go", Action: types.ActionAutoFix, Description: "x"},
	}, "1 finding")

	tripped := stepView{Name: "review", Status: "fix_review", FindingsJSON: actionable, ConvergenceJSON: trippedConvergenceJSON()}
	if _, _, resolved := gateResolution(tripped, 1); resolved {
		t.Fatal("tripped guard with actionable findings must not be auto-resolved")
	}

	healthy := stepView{Name: "review", Status: "fix_review", FindingsJSON: actionable, ConvergenceJSON: `{"round_findings":[3,1],"review_ms":60000}`}
	action, ids, resolved := gateResolution(healthy, 1)
	if !resolved || action != types.ActionFix || len(ids) != 1 {
		t.Fatalf("healthy gate should fund a fix as before, got action=%v ids=%v resolved=%v", action, ids, resolved)
	}

	// A tripped guard on a gate whose findings are all non-actionable is
	// still approved: the loop is ending, there is nothing to adjudicate.
	clean := stepView{
		Name: "review", Status: "fix_review",
		FindingsJSON:    findingsJSON(t, []types.Finding{{ID: "n1", Severity: "info", Action: types.ActionNoOp, Description: "note"}}, "clean"),
		ConvergenceJSON: trippedConvergenceJSON(),
	}
	action, _, resolved = gateResolution(clean, 1)
	if !resolved || action != types.ActionApprove {
		t.Fatalf("non-actionable gate should approve even when tripped, got action=%v resolved=%v", action, resolved)
	}
}
