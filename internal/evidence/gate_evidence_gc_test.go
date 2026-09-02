package evidence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGateGCProtectionIsNamespaceSpecific proves the retention policy does real
// work rather than merely appearing in the gate config: a reviewed commit whose
// only remaining trail is the gate-evidence reflog survives an aggressive gc,
// while the identically-shaped commit under a control namespace is pruned by the
// repository's ordinary reflog policy. Without the namespace-scoped
// gc.refs/gate-evidence/*.reflogExpire* settings both commits are pruned.
func TestGateGCProtectionIsNamespaceSpecific(t *testing.T) {
	gate, work := newRepoWithRemote(t)
	if err := ConfigureGateGCProtection(context.Background(), gate); err != nil {
		t.Fatalf("ConfigureGateGCProtection: %v", err)
	}
	// Make the surrounding repository maximally hostile: every reflog outside
	// the protected namespace expires immediately.
	runGit(t, gate, "config", "gc.reflogExpire", "now")
	runGit(t, gate, "config", "gc.reflogExpireUnreachable", "now")

	reviewed := commitOnBranch(t, work, "reviewed", "reviewed tree\n")
	control := commitOnBranch(t, work, "control", "control tree\n")
	later := commitOnBranch(t, work, "later", "later tree\n")
	runGit(t, gate, "fetch", work,
		reviewed+":refs/tmp/reviewed", control+":refs/tmp/control", later+":refs/tmp/later")

	// Seed each namespace with its commit, then move both refs forward so the
	// seeded commits are reachable only through their reflogs.
	runGit(t, gate, "update-ref", "--create-reflog", "refs/gate-evidence/RUN/1", reviewed, zeroObjectID)
	runGit(t, gate, "update-ref", "--create-reflog", "refs/control/RUN/1", control, zeroObjectID)
	runGit(t, gate, "update-ref", "refs/gate-evidence/RUN/1", later, reviewed)
	runGit(t, gate, "update-ref", "refs/control/RUN/1", later, control)
	for _, ref := range []string{"refs/tmp/reviewed", "refs/tmp/control", "refs/tmp/later"} {
		runGit(t, gate, "update-ref", "-d", ref)
	}

	runGit(t, gate, "gc", "--prune=now", "--aggressive")

	runGit(t, gate, "cat-file", "-e", reviewed+"^{commit}")
	runGitFails(t, gate, "cat-file", "-e", control+"^{commit}")
}

// commitOnBranch adds one commit on a fresh branch off main and returns its SHA.
func commitOnBranch(t *testing.T, work, branch, content string) string {
	t.Helper()
	runGit(t, work, "checkout", "-q", "main")
	runGit(t, work, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, work, "commit", "-am", branch)
	sha := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "-q", "main")
	return sha
}
