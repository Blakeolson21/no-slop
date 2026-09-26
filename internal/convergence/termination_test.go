package convergence

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestTerminalLimits(t *testing.T) {
	same := finding("a.go", "missing timeout on outbound webhook client")
	noop := same
	noop.Action = types.ActionNoOp
	for _, tc := range []struct {
		name     string
		findings []types.Finding
		limits   Thresholds
		stop     bool
	}{
		{"before cap", []types.Finding{same, same}, Thresholds{MaxRounds: 3}, false},
		{"at cap", []types.Finding{same, same}, Thresholds{MaxRounds: 2}, true},
		{"explicitly disabled", []types.Finding{same, same}, Thresholds{}, false},
		{"clean at cap", []types.Finding{same, {}}, Thresholds{MaxRounds: 2, MaxRecurringRounds: 2}, false},
		{"no-op at cap", []types.Finding{same, noop}, Thresholds{MaxRounds: 2, MaxRecurringRounds: 2}, false},
		{"no-op history not recurrence", []types.Finding{noop, same}, Thresholds{MaxRecurringRounds: 2}, false},
		{"one occurrence limit", []types.Finding{same}, Thresholds{MaxRecurringRounds: 1}, true},
		{"recurrence", []types.Finding{same, same}, Thresholds{MaxRecurringRounds: 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rounds []*db.StepRound
			for i, f := range tc.findings {
				rounds = append(rounds, makeRound(t, i+1, 1, f))
			}
			report := BuildReport(rounds, nil, false, tc.limits)
			if (report.StopReason != "") != tc.stop {
				t.Fatalf("stop reason=%q", report.StopReason)
			}
			if tc.stop && !strings.Contains(report.RedesignTicket, "Acceptance seeds") {
				t.Fatal("missing ticket")
			}
		})
	}
}
