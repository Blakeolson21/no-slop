package convergence

import (
	"encoding/json"
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
				if f.Description == "" {
					rounds = append(rounds, makeRound(t, i+1, 1))
				} else {
					rounds = append(rounds, makeRound(t, i+1, 1, f))
				}
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

// A ticket can be larger than an IPC frame. Snapshots carry its local path,
// while the full acceptance seeds remain available to the executor for export.
func TestTerminalTicketSnapshotContainsOnlyArtifactPath(t *testing.T) {
	report := BuildReport([]*db.StepRound{makeRound(t, 1, 1, finding("a.go", strings.Repeat("timeout detail ", 100000)))}, nil, false, Thresholds{MaxRounds: 1})
	report.RedesignTicketPath = "/logs/run/redesign.md"
	if len(report.RedesignTicket) < 1<<20 {
		t.Fatal("fixture must exceed an IPC frame")
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 1024 {
		t.Fatalf("snapshot includes ticket body: %d bytes", len(payload))
	}
	restored, ok := ParseReport(string(payload))
	if !ok || restored.RedesignTicketPath != report.RedesignTicketPath || restored.RedesignTicket != "" {
		t.Fatalf("snapshot contract: %+v", restored)
	}
}

func TestTerminalTicketRedactsCredentialsBeforeClassLabels(t *testing.T) {
	f := finding("a.go", "https://aaa:aaasecret@example.com/repo")
	report := BuildReport([]*db.StepRound{makeRound(t, 1, 1, f), makeRound(t, 2, 1, f)}, nil, false, Thresholds{MaxRecurringRounds: 2})
	if report.StopReason == "" {
		t.Fatal("recurrence must still be recognized")
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "aaasecret") {
		t.Fatal("snapshot exposes credential through derived class label")
	}
	if strings.Contains(report.RedesignTicket, "aaasecret") {
		t.Fatal("ticket exposes a URL credential through its derived class label")
	}
}
