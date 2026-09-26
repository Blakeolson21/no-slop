package steps

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
)

func TestNoFixCIFailureParksWithoutRepair(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, base, head, config.Commands{})
	sctx.Env = fakeCIGH(t, "OPEN", `[{"name":"test","state":"FAILURE","bucket":"fail"}]`)
	pr := "https://github.com/test/repo/pull/42"
	sctx.Run.PRURL = &pr
	sctx.Run.NoFix = true
	sctx.Config.AutoFix.CI = 3
	sctx.Config.CITimeout = 3 * time.Second
	step := &CIStep{waitForNextPoll: func(context.Context, time.Duration) error { return nil }}
	out, err := step.Execute(sctx)
	if err != nil || out == nil || !out.NeedsApproval || len(ag.calls) != 0 {
		t.Fatalf("no-fix CI: %+v %v (%d agent calls)", out, err, len(ag.calls))
	}
}
