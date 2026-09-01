//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The lease binary is an operator-supplied script, so it can leave a
// background child holding the stdout it inherited. Reading that stdout with a
// bare cmd.Run blocks on EOF forever and wedges the agent invocation that
// asked for a seat; the surviving child also outlives the daemon's step.
func TestCommandQuartermasterAcquireReturnsAndReapsALeaseScriptsBackgroundChild(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	bin := filepath.Join(root, "quartermaster")
	script := `#!/bin/sh
if [ "$1" = "acquire" ]; then
  sh -c 'echo $$ > "` + pidFile + `.tmp"; mv "` + pidFile + `.tmp" "` + pidFile + `"; sleep 120' &
  while [ ! -s "` + pidFile + `" ]; do sleep 0.01; done
  printf 'ACCOUNT=seat-a\nLEASE_ID=lease-1\n'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write quartermaster fixture: %v", err)
	}

	type acquired struct {
		lease QuartermasterLease
		err   error
	}
	done := make(chan acquired, 1)
	go func() {
		lease, err := NewCommandQuartermasterClient(bin).Acquire(context.Background(), QuartermasterAcquireRequest{
			Pool:    "codex",
			Holder:  "test-holder",
			Purpose: "review",
			TTL:     time.Minute,
		})
		done <- acquired{lease: lease, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Acquire: %v", got.err)
		}
		if got.lease.ID != "lease-1" || got.lease.Account != "seat-a" {
			t.Fatalf("lease = %+v, want the seat the script granted", got.lease)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Acquire never returned: a background child holding the lease script's stdout wedged the agent invocation")
	}

	pid := waitForRecordedPID(t, pidFile)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("lease script's background child %d outlived the acquire that spawned it", pid)
}

func waitForRecordedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lease script never recorded its background child pid at %s", path)
	return 0
}
