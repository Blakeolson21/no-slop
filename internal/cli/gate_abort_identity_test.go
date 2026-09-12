package cli

import (
	"strings"
	"testing"
)

func TestGateAbortRequiresExactNonemptyID(t *testing.T) {
	for _, args := range [][]string{{"axi", "abort"}, {"axi", "abort", "--run", ""}, {"axi", "abort", "--run", "   "}} {
		out, err := executeCmd(args...)
		if err == nil {
			t.Fatalf("missing exact ID accepted: %v %s", args, out)
		}
	}
	out, err := executeCmd("axi", "abort", "--run", "definitely-not-a-run")
	if err != nil || !strings.Contains(out, "aborted: false") || strings.Contains(out, "run_status:") {
		t.Fatalf("unknown ID claimed aborted: %v %s", err, out)
	}
}

func TestGateAbortMissingIDCannotCancelCurrentActiveRun(t *testing.T) {
	for _, args := range [][]string{{"axi", "abort"}, {"axi", "abort", "--run", ""}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cancelled := false
			newAbortQuiescenceFixture(t, runningRunForever, func() { cancelled = true })
			out, err := executeCmd(args...)
			if err == nil || cancelled || strings.Contains(out, "cancellation was requested") {
				t.Fatalf("implicit cancellation reached backend: %v %s", err, out)
			}
		})
	}
}
