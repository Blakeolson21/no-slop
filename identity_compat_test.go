package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestCanonicalAndLegacyBinaryInvocationsHaveParity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds command binaries")
	}
	dir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	canonical := filepath.Join(dir, "no-slop"+ext)
	legacy := filepath.Join(dir, "no-mistakes"+ext)
	for _, build := range []struct {
		output string
		pkg    string
	}{
		{canonical, "./cmd/no-slop"},
		{legacy, "./cmd/no-mistakes"},
	} {
		identityCommandOutput(t, 2*time.Minute, "", nil, "go", "build", "-o", build.output, build.pkg)
	}

	root := t.TempDir()
	run := func(path string) string {
		t.Helper()
		data := identityCommandOutput(t, 30*time.Second, "", append(os.Environ(), "NS_HOME="+root, "NM_HOME="+root, "NS_TELEMETRY=off", "NO_MISTAKES_TELEMETRY=off"), path, "--help")
		return string(data)
	}
	if got, want := run(legacy), run(canonical); got != want {
		t.Fatalf("legacy and canonical help differ\nlegacy:\n%s\ncanonical:\n%s", got, want)
	}
}

func TestLiveRunStateSurvivesLegacyDaemonAndCanonicalBinary(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds command binaries and starts an isolated daemon")
	}
	dir := t.TempDir()
	canonical := filepath.Join(dir, "no-slop")
	legacy := filepath.Join(dir, "no-mistakes")
	for _, build := range []struct {
		output string
		pkg    string
	}{
		{canonical, "./cmd/no-slop"},
		{legacy, "./cmd/no-mistakes"},
	} {
		identityCommandOutput(t, 2*time.Minute, "", nil, "go", "build", "-o", build.output, build.pkg)
	}

	shortDir, err := os.MkdirTemp("/tmp", "ns-identity-")
	if err != nil {
		t.Fatalf("create short daemon root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
	stateRoot := filepath.Join(shortDir, "state")
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	legacyDaemon := exec.CommandContext(daemonCtx, legacy, "daemon", "run")
	legacyDaemon.Env = identityTestEnv("NM_HOME=" + stateRoot)
	legacyDaemon.WaitDelay = 5 * time.Second
	var legacyStderr bytes.Buffer
	legacyDaemon.Stderr = &legacyStderr
	if err := legacyDaemon.Start(); err != nil {
		t.Fatalf("start legacy daemon: %v", err)
	}
	defer func() {
		_, _ = identityCommandOutputAllowError(10*time.Second, "", identityTestEnv("NS_HOME="+stateRoot), canonical, "daemon", "stop", "--force", "--abandon-executing-runs")
		cancelDaemon()
		waitIdentityCommand(t, legacyDaemon, 10*time.Second)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := identityCommandOutputAllowError(5*time.Second, "", identityTestEnv("NS_HOME="+stateRoot), canonical, "daemon", "status")
		if err == nil && strings.Contains(string(data), "daemon running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("canonical binary did not find legacy daemon: %v\n%slegacy daemon stderr:\n%s", err, data, legacyStderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	identityCommandOutput(t, 30*time.Second, repoDir, nil, "git", "init", "-b", "main")
	resolvedRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}

	p := paths.WithRoot(stateRoot)
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open shared state: %v", err)
	}
	repo, err := database.InsertRepo(resolvedRepoDir, "git@example.test:owner/repo.git", "main")
	if err != nil {
		database.Close()
		t.Fatalf("insert repo: %v", err)
	}
	run, err := database.InsertRun(repo.ID, "feature/identity", "abc123", "def456")
	if err == nil {
		err = database.UpdateRunStatus(run.ID, types.RunRunning)
	}
	if err != nil {
		database.Close()
		t.Fatalf("create legacy-named live run: %v", err)
	}

	runList := func(want string) {
		t.Helper()
		data, err := identityCommandOutputAllowError(30*time.Second, repoDir, identityTestEnv("NS_HOME="+stateRoot), canonical, "runs")
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("canonical runs missing %q: %v\n%s", want, err, data)
		}
	}
	runList("running")
	if err := database.UpdateRunStatus(run.ID, types.RunCompleted); err != nil {
		database.Close()
		t.Fatalf("complete shared run state: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close shared state: %v", err)
	}
	runList("completed")
}

func identityTestEnv(entries ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(entries)+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "NS_HOME=") || strings.HasPrefix(entry, "NM_HOME=") ||
			strings.HasPrefix(entry, "NS_TELEMETRY=") || strings.HasPrefix(entry, "NO_MISTAKES_TELEMETRY=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, entries...)
	return append(env, "NS_TELEMETRY=off", "NO_MISTAKES_TELEMETRY=off")
}

func identityCommandOutput(t *testing.T, timeout time.Duration, dir string, env []string, name string, args ...string) []byte {
	t.Helper()
	data, err := identityCommandOutputAllowError(timeout, dir, env, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(name), strings.Join(args, " "), err, data)
	}
	return data
}

func identityCommandOutputAllowError(timeout time.Duration, dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	data, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		if err == nil {
			err = ctx.Err()
		} else {
			err = fmt.Errorf("%w: %v", ctx.Err(), err)
		}
	}
	return data, err
}

func waitIdentityCommand(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case <-done:
		return
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Errorf("%s did not exit after kill", filepath.Base(cmd.Path))
	}
}
