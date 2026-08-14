package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

func TestE2EScriptRejectsSetEmptyAliasConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/e2e.sh is a POSIX shell wrapper")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "scripts/e2e.sh", "-h")
	cmd.WaitDelay = 2 * time.Second
	shellenv.ConfigureShellCommand(cmd)
	cmd.Env = filteredEnv(os.Environ(),
		"NS_E2E_DAEMON_INVENTORY",
		"NM_E2E_DAEMON_INVENTORY",
		"NS_E2E_DAEMON_INVENTORY_PARENT",
		"NM_E2E_DAEMON_INVENTORY_PARENT",
		"NS_E2E_DAEMON_MAX",
		"NM_E2E_DAEMON_MAX",
	)
	cmd.Env = append(cmd.Env, "NS_E2E_DAEMON_MAX=", "NM_E2E_DAEMON_MAX=2")
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("scripts/e2e.sh timed out\n%s", output)
	}
	if err == nil || !strings.Contains(string(output), "configure the same setting with different values") {
		t.Fatalf("scripts/e2e.sh output = %q, err = %v, want alias conflict", output, err)
	}
}
