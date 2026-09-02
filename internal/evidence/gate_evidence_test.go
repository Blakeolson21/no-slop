package evidence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinReviewedTreePinsInGateAndSurvivesAggressiveGC(t *testing.T) {
	gate, work := newRepoWithRemote(t)
	if err := ConfigureGateGCProtection(context.Background(), gate); err != nil {
		t.Fatalf("ConfigureGateGCProtection: %v", err)
	}
	if got := runGit(t, gate, "config", "--get", "gc.refs/gate-evidence/*.reflogExpire"); got != "never" {
		t.Fatalf("reflog expiration = %q, want never", got)
	}
	sha := runGit(t, gate, "rev-parse", "refs/heads/main")
	result, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
		GateDir:         gate,
		RunID:           "01RUN123",
		Round:           2,
		ReviewedHeadSHA: &sha,
	})
	if err != nil {
		t.Fatalf("PinReviewedTree: %v", err)
	}
	wantRef := "refs/gate-evidence/01RUN123/2"
	if result.Ref != wantRef || result.SHA != sha || !result.Pinned {
		t.Fatalf("result = %#v, want ref %s, sha %s, pinned", result, wantRef, sha)
	}
	if got := runGit(t, gate, "rev-parse", "--verify", wantRef+"^{commit}"); got != sha {
		t.Fatalf("pinned ref = %s, want %s", got, sha)
	}
	if got := runGit(t, work, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("worktree HEAD = %s, want %s", got, sha)
	}

	if got, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
		GateDir:         gate,
		RunID:           "01RUN123",
		Round:           2,
		ReviewedHeadSHA: &sha,
	}); err != nil {
		t.Fatalf("idempotent PinReviewedTree: %v", err)
	} else if got.Ref != wantRef || got.SHA != sha {
		t.Fatalf("idempotent result = %#v", got)
	}

	runGit(t, gate, "gc", "--prune=now", "--aggressive")
	if got := runGit(t, gate, "rev-parse", "--verify", wantRef+"^{commit}"); got != sha {
		t.Fatalf("pinned ref after gc = %s, want %s", got, sha)
	}
	runGit(t, gate, "cat-file", "-e", sha+"^{commit}")
}

func TestPinReviewedTreeRejectsUnsafeAndConflictingPins(t *testing.T) {
	gate, work := newRepoWithRemote(t)
	sha := runGit(t, gate, "rev-parse", "refs/heads/main")
	for _, runID := range []string{"bad/id", "bad id", "bad?", "bad..id", "-bad"} {
		_, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
			GateDir:         gate,
			RunID:           runID,
			Round:           1,
			ReviewedHeadSHA: &sha,
		})
		if err == nil || !strings.Contains(err.Error(), "invalid gate evidence") {
			t.Errorf("run id %q error = %v, want validation error", runID, err)
		}
	}

	if err := os.WriteFile(filepath.Join(work, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "second.txt")
	runGit(t, work, "commit", "-m", "second")
	second := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, gate, "fetch", work, second+":refs/heads/second")
	if _, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
		GateDir:         gate,
		RunID:           "stable-run",
		Round:           1,
		ReviewedHeadSHA: &sha,
	}); err != nil {
		t.Fatalf("initial PinReviewedTree: %v", err)
	}
	if _, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
		GateDir:         gate,
		RunID:           "stable-run",
		Round:           1,
		ReviewedHeadSHA: &second,
	}); err == nil || !strings.Contains(err.Error(), "cannot repoint") {
		t.Fatalf("conflicting PinReviewedTree error = %v, want conflict", err)
	}
}

func TestPinReviewedTreeSkipsMissingReviewedHead(t *testing.T) {
	gate, _ := newRepoWithRemote(t)
	result, err := PinReviewedTree(context.Background(), GateEvidencePinRequest{
		GateDir: gate,
		RunID:   "run-without-head",
		Round:   1,
	})
	if err != nil {
		t.Fatalf("PinReviewedTree nil head: %v", err)
	}
	if result.Pinned || result.Ref != "" || result.SHA != "" {
		t.Fatalf("nil-head result = %#v, want unpinned", result)
	}
	if refs := runGit(t, gate, "for-each-ref", "--format=%(refname)", GateEvidenceRefPrefix); refs != "" {
		t.Fatalf("nil-head created refs: %s", refs)
	}
}
