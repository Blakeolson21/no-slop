//go:build unix

package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestClassifyFixBudgetRefusalRequiresExactProcessSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exit3 := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 3").Run()
	exit1 := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1").Run()
	const marker = "GATE_RUNNER_ERROR refused: fix budget exhausted (MO_GATE_FIX_ROUNDS=2)"
	for _, tc := range []struct {
		name   string
		err    error
		stderr string
		want   bool
	}{
		{"exact", exit3, marker + "\n", true},
		{"CRLF and unrelated diagnostic", exit3, "diagnostic\r\n" + marker + "\r\n", true},
		{"success", nil, marker, false},
		{"different exit", exit1, marker, false},
		{"no process error", errors.New(marker), marker, false},
		{"text only in error", fmt.Errorf("%w: %s", exit3, marker), "", false},
		{"exit alone", exit3, "", false},
		{"quoted marker", exit3, "repository mentions " + marker, false},
		{"trailing text", exit3, marker + " retry later", false},
		{"negative budget", exit3, "GATE_RUNNER_ERROR refused: fix budget exhausted (MO_GATE_FIX_ROUNDS=-1)", false},
		{"malformed budget", exit3, "GATE_RUNNER_ERROR refused: fix budget exhausted (MO_GATE_FIX_ROUNDS=two)", false},
		{"overflow", exit3, "GATE_RUNNER_ERROR refused: fix budget exhausted (MO_GATE_FIX_ROUNDS=999999999999999999999999999)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFixBudgetRefusal(tc.err, tc.stderr)
			var refusal *FixBudgetRefusalError
			if errors.As(got, &refusal) != tc.want {
				t.Fatalf("classification = %T %v, want refusal=%v", got, got, tc.want)
			}
			if tc.want {
				if refusal.Limit != 2 || got.Error() != tc.err.Error() || !errors.Is(got, tc.err) {
					t.Fatalf("classification lost budget or original process error: %+v", refusal)
				}
			} else if got != tc.err {
				t.Fatalf("non-refusal error changed: %v", got)
			}
		})
	}
}
