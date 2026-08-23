//go:build unix

package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/scm"
)

func TestGetPRContentReapsDescendantHoldingStdout(t *testing.T) {
	host := New(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `(sleep 30) & printf '{"title":"fix: refresh","body":"pipeline"}'`)
	}, nil, "", "test/repo")

	started := time.Now()
	content, err := host.GetPRContent(context.Background(), &scm.PR{Number: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("GetPRContent waited %s for a surviving descendant", elapsed)
	}
	if content.Title != "fix: refresh" || content.Body != "pipeline" {
		t.Fatalf("content = %#v", content)
	}
}

func TestUpdatePRReapsLeakedGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	heartbeat := filepath.Join(dir, "heartbeat")
	factory := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		script := "( i=0; while true; do echo $i > " + heartbeat + "; i=$((i+1)); sleep 0.05; done ) >/dev/null 2>&1 & " +
			"child=$!; while [ ! -f " + heartbeat + " ]; do sleep 0.01; done; echo $child > " + pidFile + "; exit 0"
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	host := New(factory, nil, "", "test/repo")

	if _, err := host.UpdatePR(context.Background(), &scm.PR{Number: "42"}, scm.PRContent{Title: "title", Body: "body"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("grandchild pid %d survived UpdatePR: heartbeat advanced from %q to %q", pid, before, after)
	}
}
