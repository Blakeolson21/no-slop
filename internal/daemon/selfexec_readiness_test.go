package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestStartDetachedDaemonDetectsChildExitPromptly(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_TEST_START_DAEMON", "1")
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "exit")
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")

	oldHealth := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	t.Cleanup(func() { daemonHealthCheck = oldHealth })

	oldStartTime := daemonProcessStartTime
	startedPID := 0
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		startedPID = pid
		return oldStartTime(pid)
	}
	t.Cleanup(func() { daemonProcessStartTime = oldStartTime })

	started := time.Now()
	err := startDetachedDaemon(p)
	if err == nil {
		t.Fatal("startDetachedDaemon should report the child exit")
	}
	if !strings.Contains(err.Error(), "exited before readiness") || !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("startDetachedDaemon error = %v, want prompt child exit status", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("child exit detection took %v, want prompt failure", elapsed)
	}
	assertTestDaemonNotRunning(t, startedPID)
}

func TestWaitForManagedDaemonStartDetectsPublishedChildExit(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "NM_DAEMON_HELPER_PROCESS=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		close(childDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(time.Second):
		}
	})

	startedAt, err := daemonProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDaemonPIDFile(p.PIDFile(), daemonPIDFile{PID: cmd.Process.Pid, StartedAt: startedAt.UTC()}); err != nil {
		t.Fatal(err)
	}
	oldHealth := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	t.Cleanup(func() { daemonHealthCheck = oldHealth })

	started := time.Now()
	err = waitForDaemonStart(p, 0, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "managed daemon child") || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("waitForDaemonStart error = %v, want managed child exit", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("managed child exit detection took %v, want prompt failure", elapsed)
	}
}

func TestWaitForManagedDaemonStartDetectsExitAfterPIDFileRemoval(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")

	oldHealth := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	t.Cleanup(func() { daemonHealthCheck = oldHealth })

	oldInspect := inspectManagedDaemonService
	checks := 0
	inspectManagedDaemonService = func(*paths.Paths, managedServiceLaunch) (managedServiceState, error) {
		checks++
		if checks < 3 {
			return managedServiceRunning, nil
		}
		return managedServiceExited, nil
	}
	t.Cleanup(func() { inspectManagedDaemonService = oldInspect })

	started := time.Now()
	err := waitForDaemonStart(p, 0, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "managed daemon exited before readiness") {
		t.Fatalf("waitForDaemonStart error = %v, want managed service exit", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("managed service exit detection took %v, want prompt failure", elapsed)
	}
	if _, err := os.Stat(p.PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("PID file should remain absent, got %v", err)
	}
}

func TestWaitForManagedDaemonStartDetectsExitBeforePIDObservation(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "NM_DAEMON_HELPER_PROCESS=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	startedAt, err := daemonProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDaemonPIDFile(p.PIDFile(), daemonPIDFile{PID: cmd.Process.Pid, StartedAt: startedAt.UTC()}); err != nil {
		t.Fatal(err)
	}
	// Force the race from the published-child test: the child publishes its
	// identity but exits before the readiness waiter can inspect its start time.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	oldHealth, oldInspect := daemonHealthCheck, inspectManagedDaemonService
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	inspectManagedDaemonService = func(*paths.Paths, managedServiceLaunch) (managedServiceState, error) {
		return managedServiceUnknown, nil
	}
	t.Cleanup(func() {
		daemonHealthCheck, inspectManagedDaemonService = oldHealth, oldInspect
	})

	started := time.Now()
	err = waitForDaemonStart(p, 0, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "managed daemon child") || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("waitForDaemonStart error = %v, want managed child exit", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("already exited child detection took %v, want prompt failure", elapsed)
	}
}

func TestWaitForManagedDaemonStartDoesNotInferExitFromInspectionError(t *testing.T) {
	for _, livenessErr := range []error{nil, fmt.Errorf("liveness unavailable")} {
		t.Run(fmt.Sprint(livenessErr), func(t *testing.T) {
			p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
			t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")
			const childPID = 65001
			if err := writeDaemonPIDFile(p.PIDFile(), daemonPIDFile{PID: childPID, StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			oldHealth, oldInspect := daemonHealthCheck, inspectManagedDaemonService
			oldStartTime, oldRunning := daemonProcessStartTime, daemonProcessRunning
			t.Cleanup(func() {
				daemonHealthCheck, inspectManagedDaemonService = oldHealth, oldInspect
				daemonProcessStartTime, daemonProcessRunning = oldStartTime, oldRunning
			})
			checks := 0
			daemonHealthCheck = func(*paths.Paths) (bool, error) {
				checks++
				return checks > 1, nil
			}
			inspectManagedDaemonService = func(*paths.Paths, managedServiceLaunch) (managedServiceState, error) {
				return managedServiceUnknown, nil
			}
			daemonProcessStartTime = func(int) (time.Time, error) {
				return time.Time{}, fmt.Errorf("start time unavailable")
			}
			daemonProcessRunning = func(int) (bool, error) { return livenessErr == nil, livenessErr }
			if err := waitForDaemonStart(p, 0, time.Time{}); err != nil {
				t.Fatalf("inspection uncertainty must allow readiness to arrive: %v", err)
			}
		})
	}
}

func TestStartDetachedDaemonTimeoutKillsAndReapsChild(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_TEST_START_DAEMON", "1")
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "block")
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "40ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "5ms")

	oldHealth := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	t.Cleanup(func() { daemonHealthCheck = oldHealth })

	oldStartTime := daemonProcessStartTime
	startedPID := 0
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		startedPID = pid
		return oldStartTime(pid)
	}
	t.Cleanup(func() { daemonProcessStartTime = oldStartTime })

	err := startDetachedDaemon(p)
	if err == nil || !strings.Contains(err.Error(), "launched but did not become ready") {
		t.Fatalf("startDetachedDaemon error = %v, want readiness timeout", err)
	}
	assertTestDaemonNotRunning(t, startedPID)
}

func TestStartPreservesManagedAndDetachedFallbackErrors(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_TEST_START_DAEMON", "1")
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "exit")
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "3s")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "10ms")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if command == "systemctl --user start "+systemdServiceName(p) {
			return nil, fmt.Errorf("user manager unavailable")
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }

	err := Start(p)
	if err == nil {
		t.Fatal("Start should fail when managed launch and detached fallback both fail")
	}
	for _, fragment := range []string{
		"managed startup failed",
		"user manager unavailable",
		"detached fallback failed",
		"exited before readiness",
		"exit status 23",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("Start error %q missing %q", err, fragment)
		}
	}
}

func assertTestDaemonNotRunning(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("test did not capture child pid")
	}
	running, err := daemonProcessRunning(pid)
	if err != nil {
		t.Fatalf("check child pid %d: %v", pid, err)
	}
	if running {
		t.Fatalf("daemon child pid %d survived failed startup", pid)
	}
}
