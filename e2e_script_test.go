package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestE2EScriptUsesLegacyDefaultInventoryParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/e2e.sh is a POSIX shell wrapper")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go-env.log")
	writeExecutable(t, filepath.Join(fakeBin, "go"), `#!/bin/sh
{
  printf 'cmd=%s\n' "$1"
  printf 'parent=%s\n' "${NS_E2E_DAEMON_INVENTORY_PARENT:-}"
  printf 'legacy_parent=%s\n' "${NM_E2E_DAEMON_INVENTORY_PARENT:-}"
} >> "$NS_E2E_SCRIPT_ENV_LOG"
exit 0
`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "scripts/e2e.sh", "-run", "NoSuch")
	cmd.WaitDelay = 2 * time.Second
	shellenv.ConfigureShellCommand(cmd)
	cmd.Env = filteredEnv(os.Environ(),
		"PATH",
		"NS_E2E_DAEMON_INVENTORY",
		"NM_E2E_DAEMON_INVENTORY",
		"NS_E2E_DAEMON_INVENTORY_PARENT",
		"NM_E2E_DAEMON_INVENTORY_PARENT",
		"NS_E2E_DAEMON_MAX",
		"NM_E2E_DAEMON_MAX",
		"NS_E2E_SCRIPT_ENV_LOG",
	)
	cmd.Env = append(cmd.Env,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NS_E2E_SCRIPT_ENV_LOG="+logPath,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("scripts/e2e.sh timed out\n%s", output)
	}
	if err != nil {
		t.Fatalf("scripts/e2e.sh failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/no-mistakes-e2e-inventories-") {
		t.Fatalf("go env log = %q, want legacy default inventory parent", data)
	}
	if strings.Contains(string(data), "/no-slop-e2e-inventories-") {
		t.Fatalf("go env log = %q, want no canonical physical parent", data)
	}
}
