package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

type capacityProbeStep struct {
	name    types.StepName
	started chan<- string
	release <-chan struct{}
}

func (s *capacityProbeStep) Name() types.StepName { return s.name }
func (s *capacityProbeStep) Execute(ctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	if (ctx.Run.Branch == "suite") != (s.name == types.StepTest) {
		return &pipeline.StepOutcome{}, nil
	}
	s.started <- ctx.Run.Branch
	select {
	case <-s.release:
		return &pipeline.StepOutcome{}, nil
	case <-ctx.Ctx.Done():
		return nil, ctx.Ctx.Err()
	}
}

func TestPushReceivedAppliesSeparateHostCapacity(t *testing.T) {
	started := make(chan string, 4)
	releaseReview := make(chan struct{})
	releaseSuite := make(chan struct{})
	defer close(releaseSuite)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{
			&capacityProbeStep{types.StepReview, started, releaseReview},
			&capacityProbeStep{types.StepTest, started, releaseSuite},
		}
	})
	cfg, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), append(cfg, []byte("\nconcurrency: {reviews: 1, suites: 1}\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	_, head := setupTestGitRepo(t, p, d, "capacity-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	push := func(branch string) {
		t.Helper()
		var result ipc.PushReceivedResult
		if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{Gate: p.RepoDir("capacity-repo"), Ref: "refs/heads/" + branch, Old: "0000000000000000000000000000000000000000", New: head}, &result); err != nil {
			t.Fatal(err)
		}
	}
	push("suite")
	waitForStartedBranch(t, started, "suite")
	push("review-one")
	waitForStartedBranch(t, started, "review-one")
	push("review-two")
	select {
	case got := <-started:
		t.Fatalf("exceeded configured review capacity: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseReview)
	waitForStartedBranch(t, started, "review-two")
}
