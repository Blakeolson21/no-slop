package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// A newer-head dispatch that the cancel budget refuses must not cancel the run
// already on the lane. The run creation path used to cancel the active run
// first and only afterwards hit the INSERT-time budget trigger, so an
// unauthorized dispatch destroyed an authorized gate on its way to being
// refused. Admission is now one transaction that runs before any cancellation.
func TestRefusedDispatchDoesNotCancelTheActiveRun(t *testing.T) {
	started := make(chan struct{})
	slow := &mockSlowStep{name: types.StepReview, started: started}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{slow}
	})

	repo, headSHA := setupTestGitRepo(t, p, d, "budget-order-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var active ipc.PushReceivedResult
	if err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("budget-order-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &active); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first run never reached its blocking step")
	}

	// Exhaust the lane's cancel budget with two aborts that are not this run.
	for i := 0; i < 2; i++ {
		spent, e := d.InsertRun(repo.ID, "main", "spent", "base")
		if e != nil {
			t.Fatal(e)
		}
		if e = d.UpdateRunStatus(spent.ID, types.RunCancelled); e != nil {
			t.Fatal(e)
		}
	}

	// A second push on a newer head is now over budget and unadjudicated.
	gitCmd(t, repo.WorkingPath, "commit", "--allow-empty", "-m", "newer head")
	gitCmd(t, repo.WorkingPath, "push", "gate", "HEAD:refs/heads/main")
	newerSHA := gitOutput(t, repo.WorkingPath, "rev-parse", "HEAD")
	var refused ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("budget-order-repo"),
		Ref:  "refs/heads/main",
		Old:  headSHA,
		New:  newerSHA,
	}, &refused)
	if err == nil {
		t.Fatalf("over-budget dispatch admitted as run %s", refused.RunID)
	}
	if !strings.Contains(err.Error(), "cancel budget") {
		t.Fatalf("unexpected refusal: %v", err)
	}

	// The authorized run is untouched: still active, never marked cancelled,
	// and no abort was charged against the lane for it.
	run, err := d.GetRun(active.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || (run.Status != types.RunRunning && run.Status != types.RunPending) {
		t.Fatalf("the refused dispatch cancelled the active run: %+v", run)
	}
	if run.Error != nil {
		t.Fatalf("the active run was ended by a refused dispatch: %q", *run.Error)
	}
	if _, err = d.InsertRun(repo.ID, "other-lane", "head", "base"); err != nil {
		t.Fatalf("an unrelated lane was blocked: %v", err)
	}
}
