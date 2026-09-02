//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// TestStatsRunReportsResolvedReviewerIdentity drives a real gate push through
// the real daemon and reads the identity back from the surface an operator
// actually uses, `no-slop stats --run`.
//
// The harness points the configured agent at <BinDir>/<agent>, which is a
// symlink, so this is exactly the wrapper case the record exists for: the
// report must name the symlink's resolved target rather than the configured
// wrapper path, must keep the configured kind in its own column, and must show
// the model only when the adapter actually reported one.
//
// Both native adapters emit concrete attempts from their retry loops, so these
// rows exercise the attempt-reporting path. The recorder's direct fallback for
// adapters that emit no attempts is covered by
// TestExecutor_RecordsAgentInvocationsLocally. Codex additionally reports no
// model here (it resolves one from a rollout in a real user HOME, which the
// harness does not have), so that leg is the unknown case: the report must say
// unknown rather than inferring a model from the configured kind.
func TestStatsRunReportsResolvedReviewerIdentity(t *testing.T) {
	for _, tc := range []struct {
		agent     string
		wantModel string // empty means the adapter reports no model
	}{
		{agent: "claude", wantModel: "claude-opus-4-7"},
		{agent: "codex"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			h := NewHarness(t, SetupOpts{Agent: tc.agent, Scenario: cleanReviewScenario(t)})
			if out, err := h.Run("init"); err != nil {
				t.Fatalf("nm init: %v\n%s", err, out)
			}

			const branch = "feature/reviewer-identity"
			h.CommitChange(branch, "hello.txt", "hello identity\n", "add a file to review")
			h.PushToGate(branch)

			run := h.WaitForRun(branch, 90*time.Second)
			if run.Status != types.RunCompleted {
				t.Fatalf("run status = %s, want completed (error=%v)", run.Status, run.Error)
			}

			out, err := h.Run("stats", "--run", run.ID)
			if err != nil {
				t.Fatalf("nm stats --run: %v\n%s", err, out)
			}

			configuredWrapper := filepath.Join(h.BinDir, tc.agent)
			launched, err := filepath.EvalSymlinks(h.FakeAgent)
			if err != nil {
				t.Fatalf("resolve fake agent target: %v", err)
			}
			if launched == configuredWrapper {
				t.Fatalf("harness no longer configures a symlinked wrapper, so this test proves nothing: %s", launched)
			}

			if !strings.Contains(out, "EXECUTABLE") {
				t.Fatalf("stats --run has no EXECUTABLE column:\n%s", out)
			}
			if strings.Contains(out, configuredWrapper) {
				t.Fatalf("stats --run recorded the configured wrapper %s instead of its target:\n%s", configuredWrapper, out)
			}

			// Every recorded invocation of this run must carry the same observed
			// identity: configured kind in AGENT, resolved target in EXECUTABLE,
			// and the adapter's model or an explicit unknown in MODEL.
			rows := identityRows(t, out)
			if len(rows) == 0 {
				t.Fatalf("stats --run rendered no invocation rows:\n%s", out)
			}
			wantModel := tc.wantModel
			if wantModel == "" {
				wantModel = "-"
			}
			for _, row := range rows {
				if row.agent != tc.agent {
					t.Fatalf("configured agent kind = %q, want %q:\n%s", row.agent, tc.agent, out)
				}
				if row.executable != launched {
					t.Fatalf("executable = %q, want %q:\n%s", row.executable, launched, out)
				}
				if row.model != wantModel {
					t.Fatalf("model = %q, want %q:\n%s", row.model, wantModel, out)
				}
			}
		})
	}
}

type identityRow struct {
	agent      string
	executable string
	model      string
}

// identityRows reads the AGENT, EXECUTABLE, and MODEL cells out of the first
// table of the `stats --run` report. That report is this command's generated
// output contract; the cells are located by their header position rather than
// by searching the text, so a column that stops being populated fails here.
func identityRows(t *testing.T, out string) []identityRow {
	t.Helper()
	lines := strings.Split(out, "\n")
	header := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "STEP") && strings.Contains(line, "EXECUTABLE") {
			header = i
			break
		}
	}
	if header < 0 {
		t.Fatalf("stats --run has no invocation table header:\n%s", out)
	}
	columns := strings.Fields(lines[header])
	agentAt := indexOf(t, columns, "AGENT")
	executableAt := indexOf(t, columns, "EXECUTABLE")
	modelAt := indexOf(t, columns, "MODEL")

	var rows []identityRow
	for _, line := range lines[header+1:] {
		fields := strings.Fields(line)
		if len(fields) <= modelAt {
			break
		}
		rows = append(rows, identityRow{
			agent:      fields[agentAt],
			executable: fields[executableAt],
			model:      fields[modelAt],
		})
	}
	return rows
}

func indexOf(t *testing.T, values []string, want string) int {
	t.Helper()
	for i, v := range values {
		if v == want {
			return i
		}
	}
	t.Fatalf("column %q not found in %v", want, values)
	return -1
}
