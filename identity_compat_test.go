package main

import (
	"bytes"
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
		cmd := exec.Command("go", "build", "-o", build.output, build.pkg)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s from %s: %v\n%s", filepath.Base(build.output), build.pkg, err, data)
		}
	}

	root := t.TempDir()
	run := func(path string) string {
		t.Helper()
		cmd := exec.Command(path, "--help")
		cmd.Env = append(os.Environ(), "NS_HOME="+root, "NM_HOME="+root, "NS_TELEMETRY=off", "NO_MISTAKES_TELEMETRY=off")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s --help: %v\n%s", filepath.Base(path), err, data)
		}
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
		cmd := exec.Command("go", "build", "-o", build.output, build.pkg)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s from %s: %v\n%s", filepath.Base(build.output), build.pkg, err, data)
		}
	}

	shortDir, err := os.MkdirTemp("/tmp", "ns-identity-")
	if err != nil {
		t.Fatalf("create short daemon root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
	stateRoot := filepath.Join(shortDir, "state")
	legacyDaemon := exec.Command(legacy, "daemon", "run")
	legacyDaemon.Env = identityTestEnv("NM_HOME=" + stateRoot)
	var legacyStderr bytes.Buffer
	legacyDaemon.Stderr = &legacyStderr
	if err := legacyDaemon.Start(); err != nil {
		t.Fatalf("start legacy daemon: %v", err)
	}
	defer func() {
		stop := exec.Command(canonical, "daemon", "stop", "--force", "--abandon-executing-runs")
		stop.Env = identityTestEnv("NS_HOME=" + stateRoot)
		_, _ = stop.CombinedOutput()
		if legacyDaemon.ProcessState == nil {
			_ = legacyDaemon.Process.Kill()
		}
		_ = legacyDaemon.Wait()
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		status := exec.Command(canonical, "daemon", "status")
		status.Env = identityTestEnv("NS_HOME=" + stateRoot)
		data, err := status.CombinedOutput()
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
	gitInit := exec.Command("git", "init", "-b", "main")
	gitInit.Dir = repoDir
	if data, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, data)
	}
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
		cmd := exec.Command(canonical, "runs")
		cmd.Dir = repoDir
		cmd.Env = identityTestEnv("NS_HOME=" + stateRoot)
		data, err := cmd.CombinedOutput()
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
