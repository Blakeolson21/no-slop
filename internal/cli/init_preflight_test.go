package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/daemon"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

// A registration whose origin does not carry the default branch must fail at
// `no-slop init`, which is the earliest boundary an operator can see it, and
// must leave nothing behind: no gate on disk, no gate remote in the working
// clone, and no daemon started on its behalf.
func TestInitCommandRefusesOriginMissingDefaultBranch(t *testing.T) {
	repoDir := setupTestRepo(t)

	originURL, err := exec.Command("git", "-C", repoDir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		t.Fatalf("read origin url: %v", err)
	}
	origin := strings.TrimSpace(string(originURL))
	run(t, repoDir, "git", "push", "origin", "HEAD:refs/heads/release")
	run(t, "", "git", "--git-dir="+origin, "update-ref", "-d", "refs/heads/main")

	out, err := executeCmd("init")
	if err == nil {
		t.Fatalf("init exited 0, want refusal; output:\n%s", out)
	}
	t.Logf("$ no-slop init\nError: %v", err)
	if !strings.HasPrefix(err.Error(), "init: ") {
		t.Errorf("operator-visible error %q is not reported by the init command", err)
	}
	for _, want := range []string{repoDir, origin, "refs/heads/main", "refs/heads/release", "does not resolve default branch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("init error %q does not name %q", err, want)
		}
	}

	remotes, remoteErr := exec.Command("git", "-C", repoDir, "remote").Output()
	if remoteErr != nil {
		t.Fatalf("list remotes: %v", remoteErr)
	}
	if strings.Contains(string(remotes), "no-slop") {
		t.Errorf("refused init left a gate remote behind: %q", remotes)
	}

	p := paths.WithRoot(os.Getenv("NS_HOME"))
	entries, dirErr := os.ReadDir(p.ReposDir())
	if dirErr != nil && !os.IsNotExist(dirErr) {
		t.Fatalf("read gate dir: %v", dirErr)
	}
	for _, entry := range entries {
		t.Errorf("refused init provisioned gate %q", filepath.Join(p.ReposDir(), entry.Name()))
	}
	if alive, _ := daemon.IsRunning(p); alive {
		t.Error("refused init started a daemon")
	}
}

// The operator has to repair connectivity, not create a branch that was never
// missing, so an origin it cannot reach must read differently at the terminal
// than an origin that answered and lacked the branch - and must not print the
// credential the operator pasted into the remote URL.
func TestInitCommandRefusesUnreachableOriginWithoutLeakingCredential(t *testing.T) {
	repoDir := setupTestRepo(t)

	const token = "ghp_cli_unreachable_DO_NOT_LEAK"
	run(t, repoDir, "git", "remote", "set-url", "origin",
		"https://x-access-token:"+token+"@127.0.0.1:1/o/r.git")

	out, err := executeCmd("init")
	if err == nil {
		t.Fatalf("init exited 0, want refusal; output:\n%s", out)
	}
	t.Logf("$ no-slop init\nError: %v", err)
	if strings.Contains(err.Error(), token) || strings.Contains(out, token) {
		t.Errorf("unreachable-origin refusal leaked the origin credential: %q / %q", err, out)
	}
	if !strings.Contains(err.Error(), "could not reach origin") {
		t.Errorf("init error %q does not report the transport failure", err)
	}
	if strings.Contains(err.Error(), "does not resolve default branch") {
		t.Errorf("unreachable origin was reported as reachable-but-absent: %q", err)
	}
	if !strings.Contains(err.Error(), "redacted@127.0.0.1:1/o/r.git") {
		t.Errorf("init error %q does not name the redacted origin", err)
	}
}
