package daemon

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/paths"
)

// TestStart_ReinstallsManagedServiceWhenPlistChanged covers the post-upgrade
// case for #143: the user installed a newer binary that renders a different
// plist (e.g., now carrying a PATH env fix), the old daemon is still alive
// under the stale plist, and `daemon start` must refresh the plist and boot
// launchd under the new one so the fix actually takes effect. Prior behavior
// was "daemon already running" with no refresh, stranding the user.
func TestStart_ReinstallsManagedServiceWhenPlistChanged(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/opt/no-slop/bin/no-slop", nil }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("<stale-plist-from-older-binary/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	running := true
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, "launchctl bootout ") {
			running = false
		}
		if strings.Contains(command, "launchctl kickstart ") {
			running = true
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return running, nil }

	if err := Start(p); err != nil {
		t.Fatalf("Start should reload and succeed when plist changed, got %v", err)
	}

	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<key>PATH</key>") {
		t.Fatalf("expected plist to be rewritten with PATH env, got:\n%s", data)
	}
	sawBootout := false
	sawBootstrap := false
	sawKickstart := false
	for _, cmd := range commands {
		if strings.Contains(cmd, "launchctl bootout ") {
			sawBootout = true
		}
		if strings.Contains(cmd, "launchctl bootstrap ") {
			sawBootstrap = true
		}
		if strings.Contains(cmd, "launchctl kickstart ") {
			sawKickstart = true
		}
	}
	if !sawBootout || !sawBootstrap || !sawKickstart {
		t.Fatalf("expected launchctl bootout+bootstrap+kickstart during reload, got %v", commands)
	}
}

// TestStart_DoesNotReinstallWhenPlistUnchanged preserves the existing
// "daemon already running" contract when nothing has changed, so repeated
// `daemon start` invocations from shells/hooks don't silently restart the
// daemon and churn the current run.
func TestStart_DoesNotReinstallWhenPlistUnchanged(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/opt/no-slop/bin/no-slop", nil }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	current := renderLaunchAgent("/opt/no-slop/bin/no-slop", p, home)
	if err := os.WriteFile(plistPath, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected 'already running' error when plist unchanged, got %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("expected no service commands when plist unchanged, got %v", commands)
	}
}

func TestStartDoesNotRestartLaunchAgentForExecutableOnlyChange(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/private/var/folders/go-build/no-slop", nil }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	installed := renderLaunchAgent("/opt/no-slop/bin/no-slop", p, home)
	if err := os.WriteFile(plistPath, []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("expected no service restart for executable-only change, got %v", commands)
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != installed {
		t.Fatalf("plist changed for executable-only diff:\n%s", data)
	}
}

func TestStartPreservesInstalledExecutableWhenRefreshingLaunchAgent(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/private/var/folders/go-build/no-slop", nil }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := renderLaunchAgentWithoutEnvironment("/opt/no-slop/bin/no-slop", p)
	if err := os.WriteFile(plistPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	running := true
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if strings.Contains(command, "launchctl bootout ") {
			running = false
		}
		if strings.Contains(command, "launchctl kickstart ") {
			running = true
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return running, nil }

	if err := Start(p); err != nil {
		t.Fatalf("Start should refresh stale plist: %v", err)
	}

	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(data)
	if !strings.Contains(plist, "<string>/opt/no-slop/bin/no-slop</string>") {
		t.Fatalf("expected refreshed plist to preserve installed executable, got:\n%s", plist)
	}
	if strings.Contains(plist, "/private/var/folders/go-build/no-slop") {
		t.Fatalf("refreshed plist should not use transient executable:\n%s", plist)
	}
	if !strings.Contains(plist, "<key>PATH</key>") {
		t.Fatalf("expected refreshed plist to include PATH, got:\n%s", plist)
	}
}

func TestStartRestartsSystemdUnitWhenDefinitionChanged(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/old/no-slop daemon run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	running := true
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		switch command {
		case "systemctl --user stop " + systemdServiceName(p):
			running = false
		case "systemctl --user restart " + systemdServiceName(p):
			running = true
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return running, nil }

	if err := Start(p); err != nil {
		t.Fatalf("Start should restart stale systemd unit, got %v", err)
	}

	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + systemdServiceName(p),
		"systemctl --user stop " + systemdServiceName(p),
		"systemctl --user restart " + systemdServiceName(p),
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestStartStopsDetachedDaemonBeforeRestartingStaleManagedService(t *testing.T) {
	p, _ := startTestDaemon(t)
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/old/no-slop daemon run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHealthCheck := daemonHealthCheck
	var serviceRestarted atomic.Bool
	daemonHealthCheck = func(p *paths.Paths) (bool, error) {
		if serviceRestarted.Load() {
			return true, nil
		}
		alive, err := oldHealthCheck(p)
		if err != nil {
			// A racing Windows TCP connection can report an error after the
			// in-process daemon has shut down. For this service-ordering test,
			// that state deterministically means the old daemon is stopped.
			return false, nil
		}
		return alive, nil
	}

	var restartedWhileOldDaemonAlive atomic.Bool
	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user restart "+systemdServiceName(p) {
			alive, err := oldHealthCheck(p)
			if err != nil {
				t.Fatalf("health check before restart: %v", err)
			}
			if alive {
				restartedWhileOldDaemonAlive.Store(true)
			}
			serviceRestarted.Store(true)
		}
		return nil, nil
	}

	if err := Start(p); err != nil {
		t.Fatalf("Start should replace stale managed service: %v", err)
	}
	if restartedWhileOldDaemonAlive.Load() {
		t.Fatalf("managed service restarted while detached daemon was still alive; commands = %v", commands)
	}
}

func TestStartDoesNotStopRunningDaemonWhenStaleManagedInstallFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/old/no-slop daemon run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user daemon-reload" {
			return nil, fmt.Errorf("daemon-reload failed")
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return install failure")
	}
	if !strings.Contains(err.Error(), "daemon-reload failed") {
		t.Fatalf("Start error = %v, want install failure", err)
	}
	for _, command := range commands {
		if command == "systemctl --user stop "+systemdServiceName(p) {
			t.Fatalf("should not stop running daemon before install succeeds; commands = %v", commands)
		}
	}
}

func TestStartRestoresStaleSystemdUnitWhenRefreshInstallFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "[Service]\nExecStart=/old/no-slop daemon run\n"
	if err := os.WriteFile(unitPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		if name == "systemctl" && reflect.DeepEqual(args, []string{"--user", "daemon-reload"}) {
			return nil, fmt.Errorf("daemon-reload failed")
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return install failure")
	}
	data, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != stale {
		t.Fatalf("unit file = %q, want stale definition restored", string(data))
	}
}

// TestStartRestoresStaleSystemdUnitAtOriginalModeWhenRefreshInstallFails guards
// the drift-reinstall restore path against re-opening the 0644 credential leak
// that writeFileAtomic closed for the install path. A prior proxy install
// leaves the unit at 0600 with credential-bearing content (a forwarded proxy
// URL can embed user:pass). A drift reinstall from a shell without the proxy
// vars rewrites the unit at the conventional 0644; if that reinstall then
// fails, the restore writes the original 0600 credential content back - but an
// in-place os.WriteFile only re-applies its mode on create, so it would leave
// the credentials world-readable at 0644. The restore must re-apply the
// captured 0600 mode.
func TestStartRestoresStaleSystemdUnitAtOriginalModeWhenRefreshInstallFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes (0600) are not enforced on Windows; the proxy-bearing service file is only generated on macOS/Linux")
	}
	for _, key := range proxyEnvKeys {
		t.Setenv(key, "")
	}
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "[Service]\nEnvironment=\"HTTPS_PROXY=http://user:pass@127.0.0.1:7897\"\nExecStart=/old/no-slop daemon run\n"
	if err := os.WriteFile(unitPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		if name == "systemctl" && reflect.DeepEqual(args, []string{"--user", "daemon-reload"}) {
			return nil, fmt.Errorf("daemon-reload failed")
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return install failure")
	}
	data, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != stale {
		t.Fatalf("unit file = %q, want stale credential definition restored", string(data))
	}
	info, statErr := os.Stat(unitPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("restored unit mode = %o, want 0600 so forwarded proxy credentials are not world-readable", got)
	}
}

func TestStartRestartsRestoredSystemdUnitWhenRefreshRestartFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "[Service]\nExecStart=/old/no-slop daemon run\n"
	if err := os.WriteFile(unitPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	var stopped bool
	restartAttempts := 0
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		switch command {
		case "systemctl --user stop " + systemdServiceName(p):
			stopped = true
		case "systemctl --user restart " + systemdServiceName(p):
			restartAttempts++
			if restartAttempts == 1 {
				return nil, fmt.Errorf("restart failed")
			}
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		return !stopped || restartAttempts > 1, nil
	}

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return restart failure")
	}
	if !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("Start error = %v, want restart failure", err)
	}
	data, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != stale {
		t.Fatalf("unit file = %q, want stale definition restored", string(data))
	}
	if restartAttempts != 2 {
		t.Fatalf("restart attempts = %d, want new restart plus restored restart; commands = %v", restartAttempts, commands)
	}
	if len(commands) < 2 || commands[len(commands)-2] != "systemctl --user daemon-reload" {
		t.Fatalf("expected daemon-reload before restored restart; commands = %v", commands)
	}
}

func TestStartDoesNotInstallManagedServiceWhenDaemonAliveAndDefinitionMissing(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return true, nil }

	err := Start(p)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("expected no service commands, got %v", commands)
	}
	if _, statErr := os.Stat(systemdUserServicePath(p)); !os.IsNotExist(statErr) {
		t.Fatalf("expected no systemd unit to be installed, stat err = %v", statErr)
	}
}

func TestStartFallsBackToDetachedDaemonWhenManagedStartFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	var commands []string
	var managedStopped bool
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user stop "+systemdServiceName(p) {
			managedStopped = true
			return nil, nil
		}
		if command == "systemctl --user start "+systemdServiceName(p) {
			return nil, fmt.Errorf("user manager unavailable")
		}
		return nil, nil
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return checks >= 3, nil
	}

	if err := Start(p); err != nil {
		t.Fatalf("Start should fall back to detached mode: %v", err)
	}

	if len(commands) != 4 {
		t.Fatalf("expected managed start, stop, and detached fallback, got %v", commands)
	}
	if want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + systemdServiceName(p),
		"systemctl --user start " + systemdServiceName(p),
		"systemctl --user stop " + systemdServiceName(p),
	}; len(commands) == len(want) {
		for i, wantCmd := range want {
			if commands[i] != wantCmd {
				t.Fatalf("command[%d] = %q, want %q", i, commands[i], wantCmd)
			}
		}
	}
	if !managedStopped {
		t.Fatal("managed service should be stopped before detached fallback")
	}
	if _, err := os.Stat(p.DaemonBootstrapLog()); err != nil {
		t.Fatalf("detached fallback should open daemon bootstrap log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))); err != nil {
		t.Fatalf("managed service install should still write unit file: %v", err)
	}
	if pidData, err := os.ReadFile(p.PIDFile()); err == nil && len(pidData) > 0 {
		t.Fatalf("helper detached process should not leave a pid file, got %q", string(pidData))
	}
	if checks < 3 {
		t.Fatalf("expected health checks for preflight, managed failure, and detached wait, got %d", checks)
	}
	_ = os.Remove(p.DaemonLog())
	_ = os.Remove(p.PIDFile())
	_ = os.Remove(p.Socket())
}

func TestStartTreatsManagedInstallCleanupFailureAsTerminal(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }

	legacyPath := filepath.Join(home, ".config", "systemd", "user", legacySystemdServiceName)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(renderSystemdUnit("/usr/local/bin/no-mistakes", p, home)), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user stop "+legacySystemdServiceName {
			return nil, fmt.Errorf("legacy stop failed")
		}
		return nil, nil
	}

	err := Start(p)
	if err == nil || !strings.Contains(err.Error(), "install managed service") || !strings.Contains(err.Error(), "systemctl stop legacy service") {
		t.Fatalf("Start error = %v, want terminal managed cleanup failure", err)
	}
	if strings.Contains(err.Error(), "detached fallback") {
		t.Fatalf("Start attempted detached fallback after cleanup failure: %v", err)
	}
	for _, command := range commands {
		if command == "systemctl --user start "+systemdServiceName(p) {
			t.Fatalf("Start should not start canonical service after cleanup failure, commands = %v", commands)
		}
	}
}

func TestStartDetachedDaemonUsesProvidedRootViaNMHome(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "ns-home.txt")

	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	t.Setenv("NM_CAPTURE_NS_HOME_FILE", capturePath)
	t.Setenv("NS_HOME", "")

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return checks >= 2, nil
	}

	if err := startDetachedDaemon(p); err != nil {
		t.Fatalf("startDetachedDaemon should succeed: %v", err)
	}

	var (
		data []byte
		err  error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(capturePath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read captured NS_HOME: %v", err)
	}
	if got := string(data); got != p.Root() {
		t.Fatalf("child NS_HOME = %q, want %q", got, p.Root())
	}
}

func TestStartDetachedDaemonMirrorsCanonicalHelperAlias(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "ns-home.txt")

	t.Setenv(daemonHelperProcessEnv, "1")
	t.Setenv("NM_CAPTURE_NS_HOME_FILE", capturePath)
	t.Setenv("NS_HOME", "")

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return checks >= 2, nil
	}

	if err := startDetachedDaemon(p); err != nil {
		t.Fatalf("startDetachedDaemon should succeed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(capturePath)
		if err == nil {
			if got := string(data); got != p.Root() {
				t.Fatalf("child NS_HOME = %q, want %q", got, p.Root())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("canonical helper alias was not mirrored to legacy child helper")
}

func TestStartDetachedDaemonCleansUpChildWhenStartTimeProbeFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NM_DAEMON_HELPER_PROCESS", "block")

	oldStartTime := daemonProcessStartTime
	startedPID := 0
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		startedPID = pid
		return time.Time{}, fmt.Errorf("inspect failed")
	}
	t.Cleanup(func() {
		daemonProcessStartTime = oldStartTime
		if startedPID <= 0 {
			return
		}
		running, err := daemonProcessRunning(startedPID)
		if err == nil && running {
			_ = daemonKillPID(startedPID)
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				running, err = daemonProcessRunning(startedPID)
				if err == nil && !running {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	})

	err := startDetachedDaemon(p)
	if err == nil {
		t.Fatal("startDetachedDaemon should fail when process inspection fails")
	}
	if !strings.Contains(err.Error(), "inspect daemon process") {
		t.Fatalf("startDetachedDaemon error = %v, want process inspection failure", err)
	}
	if startedPID <= 0 {
		t.Fatal("expected startDetachedDaemon to spawn a child process")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running, runErr := daemonProcessRunning(startedPID)
		if runErr == nil && !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	running, runErr := daemonProcessRunning(startedPID)
	if runErr != nil {
		t.Fatalf("daemonProcessRunning(%d) returned error after inspection failure: %v", startedPID, runErr)
	}
	if running {
		t.Fatalf("child pid %d still running after inspection failure", startedPID)
	}
}

func TestStartStopsManagedServiceBeforeDetachedFallbackAfterTimeout(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "20ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "1ms")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	var commands []string
	var managedStopped bool
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user stop "+systemdServiceName(p) {
			managedStopped = true
		}
		return nil, nil
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		if !managedStopped {
			return false, nil
		}
		return checks > 2, nil
	}

	oldListProcesses := daemonListDaemonProcesses
	listChecks := 0
	daemonListDaemonProcesses = func() ([]daemonProcessInfo, error) {
		if !managedStopped {
			return nil, nil
		}
		listChecks++
		if listChecks < 3 {
			return []daemonProcessInfo{{PID: 4242, Root: p.Root()}}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { daemonListDaemonProcesses = oldListProcesses })
	oldStartTime := daemonProcessStartTime
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		if listChecks < 3 {
			t.Fatal("detached fallback launched before managed child exit was confirmed")
		}
		return oldStartTime(pid)
	}
	t.Cleanup(func() { daemonProcessStartTime = oldStartTime })

	if err := Start(p); err != nil {
		t.Fatalf("Start should fall back to detached mode after managed timeout: %v", err)
	}

	if len(commands) != 4 {
		t.Fatalf("expected managed start, stop, and detached fallback, got %v", commands)
	}
	if want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + systemdServiceName(p),
		"systemctl --user start " + systemdServiceName(p),
		"systemctl --user stop " + systemdServiceName(p),
	}; len(commands) == len(want) {
		for i, wantCmd := range want {
			if commands[i] != wantCmd {
				t.Fatalf("command[%d] = %q, want %q", i, commands[i], wantCmd)
			}
		}
	}
	if !managedStopped {
		t.Fatal("managed service should be stopped before detached fallback")
	}
	if _, err := os.Stat(p.DaemonBootstrapLog()); err != nil {
		t.Fatalf("detached fallback should open daemon bootstrap log: %v", err)
	}
	if checks < 3 {
		t.Fatalf("expected health checks during managed timeout and detached wait, got %d", checks)
	}
	_ = os.Remove(p.DaemonLog())
	_ = os.Remove(p.PIDFile())
	_ = os.Remove(p.Socket())
}

func TestStartReturnsManagedStopErrorWhenSystemdStopSaysNotLoaded(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "systemctl --user start "+systemdServiceName(p) {
			return nil, fmt.Errorf("user manager unavailable")
		}
		if command == "systemctl --user stop "+systemdServiceName(p) {
			return nil, fmt.Errorf("Unit not loaded")
		}
		return nil, nil
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return false, nil
	}

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return the managed stop error")
	}
	if !strings.Contains(err.Error(), "stop managed daemon before detached fallback") {
		t.Fatalf("Start error = %v, want managed stop failure", err)
	}
	if !strings.Contains(err.Error(), "Unit not loaded") {
		t.Fatalf("Start error = %v, want original stop error", err)
	}
	if checks < 2 {
		t.Fatalf("expected health checks before and after managed stop failure, got %d", checks)
	}
}

func TestStartReturnsManagedStopErrorWhenFallbackCleanupFails(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-slop", nil }

	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if command == "systemctl --user start "+systemdServiceName(p) {
			return nil, fmt.Errorf("user manager unavailable")
		}
		if command == "systemctl --user stop "+systemdServiceName(p) {
			return nil, fmt.Errorf("permission denied")
		}
		return nil, nil
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return false, nil
	}

	err := Start(p)
	if err == nil {
		t.Fatal("Start should return the managed stop error")
	}
	if !strings.Contains(err.Error(), "stop managed daemon before detached fallback") {
		t.Fatalf("Start error = %v, want managed stop failure", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Start error = %v, want original stop error", err)
	}
	if checks < 2 {
		t.Fatalf("expected health checks before and after managed stop failure, got %d", checks)
	}
}

func TestStartRemovesLaunchAgentBeforeDetachedFallbackAfterBootoutESRCH(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/opt/no-slop/bin/no-slop", nil }

	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		switch command {
		case "launchctl bootout gui/501/" + launchdServiceLabel(p):
			return []byte("Boot-out failed: 3: No such process"), fmt.Errorf("exit status 3: Boot-out failed: 3: No such process")
		case "launchctl bootstrap gui/501 " + launchAgentPath(p):
			return nil, nil
		case "launchctl kickstart -k gui/501/" + launchdServiceLabel(p):
			return nil, fmt.Errorf("launchctl kickstart failed")
		default:
			return nil, nil
		}
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return checks >= 3, nil
	}

	if err := Start(p); err != nil {
		t.Fatalf("Start should fall back to detached mode: %v", err)
	}

	if _, err := os.Stat(launchAgentPath(p)); !os.IsNotExist(err) {
		t.Fatalf("launch agent plist should be removed before detached fallback, stat err = %v", err)
	}
	_ = os.Remove(p.DaemonLog())
	_ = os.Remove(p.PIDFile())
	_ = os.Remove(p.Socket())
}

func TestStopUsesManagedServiceWhenInstalled(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("WorkingDirectory="+p.Root()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}

	if err := Stop(p); err != nil {
		t.Fatal(err)
	}

	if len(commands) != 1 {
		t.Fatalf("expected one stop command, got %v", commands)
	}
	if want := "systemctl --user stop " + systemdServiceName(p); commands[0] != want {
		t.Fatalf("stop command = %q, want %q", commands[0], want)
	}
}

func TestStopManagedServiceStopsCanonicalAndLegacySystemdUnits(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }

	canonicalPath := systemdUserServicePath(p)
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, []byte("WorkingDirectory="+p.Root()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyPath := legacySystemdUserServicePath()
	if err := os.WriteFile(legacyPath, []byte(renderSystemdUnit("/usr/local/bin/no-mistakes", p, home)), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}

	stopped, err := stopManagedService(p)
	if err != nil || !stopped {
		t.Fatalf("stopManagedService = (%v, %v), want (true, nil); commands %v", stopped, err, commands)
	}
	for _, want := range []string{
		"systemctl --user stop " + systemdServiceName(p),
		"systemctl --user stop " + legacySystemdServiceName,
	} {
		if !containsCmd(commands, want) {
			t.Fatalf("commands = %v, want %q", commands, want)
		}
	}
}

func TestManagedServiceInstalledRequiresMatchingRoot(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "darwin"
	serviceUserHomeDir = func() (string, error) { return home, nil }

	otherRoot := filepath.Join(t.TempDir(), "other-root")
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(paths.WithRoot(otherRoot))+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte(renderLaunchAgent("/opt/no-slop/bin/no-slop", paths.WithRoot(otherRoot), home)), 0o644); err != nil {
		t.Fatal(err)
	}

	if installed, err := managedServiceInstalled(p); err != nil {
		t.Fatal(err)
	} else if installed {
		t.Fatal("expected mismatched launch agent root to be ignored")
	}
	if installed, err := managedServiceInstalled(paths.WithRoot(otherRoot)); err != nil {
		t.Fatal(err)
	} else if !installed {
		t.Fatal("expected matching launch agent root to be detected")
	}
}

func TestStopFallsBackToDetachedDaemonWhenManagedStopFails(t *testing.T) {
	p, _ := startTestDaemon(t)
	home := t.TempDir()

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "linux"
	serviceUserHomeDir = func() (string, error) { return home, nil }

	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("WorkingDirectory="+p.Root()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, fmt.Errorf("user manager unavailable")
	}

	if err := Stop(p); err != nil {
		t.Fatalf("Stop should fall back to detached daemon shutdown: %v", err)
	}

	if len(commands) != 1 {
		t.Fatalf("expected one managed stop command before fallback, got %v", commands)
	}
	if want := "systemctl --user stop " + systemdServiceName(p); commands[0] != want {
		t.Fatalf("stop command = %q, want %q", commands[0], want)
	}

	alive, err := IsRunning(p)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon should be stopped")
	}
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("managed unit should remain installed after fallback stop: %v", err)
	}
	_ = os.Remove(unitPath)
	_ = os.Remove(filepath.Dir(unitPath))
	_ = os.Remove(filepath.Dir(filepath.Dir(unitPath)))
	_ = os.Remove(filepath.Dir(filepath.Dir(filepath.Dir(unitPath))))
}

func TestManagedStopErrorsStillWaitForCapturedDaemonExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*paths.Paths) error
	}{
		{name: "stop", stop: Stop},
		{name: "managed restart handoff", stop: stopCurrentDaemonBeforeManagedRestart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			startedAt := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
			writeDaemonPIDRecord(t, p.PIDFile(), daemonPIDFile{PID: 4242, StartedAt: startedAt})

			cleanup := stubServiceRuntime(t)
			defer cleanup()
			runtimeGOOS = "linux"
			serviceUserHomeDir = func() (string, error) { return home, nil }
			daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }

			unitPath := filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitPath, []byte("WorkingDirectory="+p.Root()+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			serviceCommandRunner = func(string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("user manager unavailable")
			}
			oldStartTime := daemonProcessStartTime
			oldProcessRunning := daemonProcessRunning
			daemonProcessStartTime = func(pid int) (time.Time, error) {
				if pid != 4242 {
					t.Fatalf("daemonProcessStartTime pid = %d, want 4242", pid)
				}
				return startedAt, nil
			}
			probes := 0
			daemonProcessRunning = func(pid int) (bool, error) {
				if pid != 4242 {
					t.Fatalf("daemonProcessRunning pid = %d, want 4242", pid)
				}
				probes++
				return false, nil
			}
			t.Cleanup(func() {
				daemonProcessStartTime = oldStartTime
				daemonProcessRunning = oldProcessRunning
			})

			err := tc.stop(p)
			if err == nil || !strings.Contains(err.Error(), "user manager unavailable") {
				t.Fatalf("stop error = %v, want managed-service failure after confirming daemon exit", err)
			}
			if probes == 0 {
				t.Fatal("stop returned without probing the captured daemon process")
			}
		})
	}
}

func TestStopFallsBackToDetachedDaemonOnWindowsWithoutManagedService(t *testing.T) {
	p, _ := startTestDaemon(t)

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"
	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, fmt.Errorf("task not found")
	}

	if err := Stop(p); err != nil {
		t.Fatalf("Stop should fall back to detached daemon shutdown: %v", err)
	}
	if len(commands) != 3 {
		t.Fatalf("expected canonical and compatibility scheduled-task queries, got %v", commands)
	}
	if want := "schtasks /Query /TN " + windowsTaskName(p); commands[0] != want {
		t.Fatalf("query command = %q, want %q", commands[0], want)
	}
	if want := "schtasks /Query /TN " + legacyScopedWindowsTaskName(p) + " /XML"; commands[1] != want {
		t.Fatalf("scoped legacy query = %q, want %q", commands[1], want)
	}
	if want := "schtasks /Query /TN " + legacyWindowsTaskName + " /XML"; commands[2] != want {
		t.Fatalf("unscoped legacy query = %q, want %q", commands[2], want)
	}

	alive, err := IsRunning(p)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon should be stopped")
	}
}

func TestStopManagedServiceFindsLegacyScopedWindowsTask(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, "/Query /TN "+windowsTaskName(p)) {
			return nil, fmt.Errorf("task not found")
		}
		if strings.Contains(command, "/Query /TN "+legacyScopedWindowsTaskName(p)+" /XML") {
			return []byte(`<Task><Arguments>daemon run --root ` + p.Root() + `</Arguments></Task>`), nil
		}
		return nil, nil
	}

	stopped, err := stopManagedService(p)
	if err != nil || !stopped {
		t.Fatalf("stop legacy scoped task = (%v, %v), want (true, nil); commands %v", stopped, err, commands)
	}
	wantEnd := "schtasks /End /TN " + legacyScopedWindowsTaskName(p)
	if !containsCmd(commands, wantEnd) {
		t.Fatalf("commands = %v, want %q", commands, wantEnd)
	}
}

// TestDefaultServiceManagerBypassedIsTrueUnderGoTest locks in the contract
// that protects developer machines: under `go test`, the default bypass
// function must short-circuit managed-service plumbing so unstubbed daemon
// tests cannot invoke real launchctl/systemctl/schtasks. A regression here
// previously caused TestStopNotRunningIsNoop to tear down the live
// LaunchAgent-managed daemon on macOS.
func TestDefaultServiceManagerBypassedIsTrueUnderGoTest(t *testing.T) {
	bypassed, err := defaultServiceManagerBypassed()
	if err != nil {
		t.Fatal(err)
	}
	if !bypassed {
		t.Fatal("defaultServiceManagerBypassed() must return true under `go test` so daemon tests cannot reach real launchctl/systemctl/schtasks state")
	}
}

// TestStopWithUnstubbedPathsDoesNotInvokeRealServiceCommands is the
// end-to-end regression test: it simulates a managed service having been
// installed at the user's real-looking home (plist / systemd unit) and
// asserts that Stop(p), when called from a test binary with only a temp
// paths.Paths, does not invoke any service-manager commands. Before the
// fix, Stop went through stopManagedService -> managedServiceInstalled ->
// os.Stat(launchAgentPath()) which used the real os.UserHomeDir, found the
// real plist, then ran real `launchctl bootout` and killed the live daemon.
func TestStopWithUnstubbedPathsDoesNotInvokeRealServiceCommands(t *testing.T) {
	cleanup := stubServiceRuntime(t)
	defer cleanup()

	// Unlike other tests using stubServiceRuntime, we deliberately restore
	// the production bypass function. That is the code under test here.
	serviceManagerBypassed = defaultServiceManagerBypassed

	home := t.TempDir()
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "99999"}, nil }
	runtimeGOOS = runtime.GOOS

	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	// Seed a managed-service artifact at the SCOPED path for this p so that
	// if the testing.Testing() bypass ever regresses, managedServiceInstalled(p)
	// would return true and Stop(p) would call serviceCommandRunner. We detect
	// that below.
	switch runtime.GOOS {
	case "darwin":
		plistDir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(plistDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plistDir, launchdServiceLabel(p)+".plist"), []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "linux":
		unitDir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unitDir, systemdServiceName(p)), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var called []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		called = append(called, name+" "+strings.Join(args, " "))
		// Pretend the task/service exists on Windows so the "is installed"
		// probe cannot short-circuit via an error return.
		return nil, nil
	}

	if err := Stop(p); err != nil {
		t.Fatalf("Stop(p) with unstubbed paths must be a no-op under go test, got error: %v", err)
	}
	if len(called) > 0 {
		t.Fatalf("Stop(p) under go test must not invoke any service-manager commands, got: %v", called)
	}
}

// TestStartWithUnstubbedPathsDoesNotInvokeRealServiceCommands mirrors the
// Stop regression for Start(). Start also goes through
// installManagedService -> startManagedService, both of which rely on the
// bypass guard to stay out of real launchctl/systemctl/schtasks when called
// from a test binary with only a temp paths.Paths.
func TestStartWithUnstubbedPathsDoesNotInvokeRealServiceCommands(t *testing.T) {
	cleanup := stubServiceRuntime(t)
	defer cleanup()

	serviceManagerBypassed = defaultServiceManagerBypassed

	// Force the detached fallback path to short-circuit as well: TestMain
	// already exits immediately when NM_DAEMON_HELPER_PROCESS=1, so the
	// re-exec does not spawn a persistent daemon.
	t.Setenv("NM_DAEMON_HELPER_PROCESS", "1")

	home := t.TempDir()
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "99999"}, nil }
	serviceExecutablePath = func() (string, error) { return os.Args[0], nil }
	runtimeGOOS = runtime.GOOS

	var called []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		called = append(called, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	// Report "not running" for all health checks so Start exercises the
	// full managed-install-then-fallback decision tree.
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }

	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	// Start will fall back to the detached daemon which re-execs the test
	// binary; with NM_DAEMON_HELPER_PROCESS=1 the helper exits and the
	// health check never returns ok, so Start reports "did not become
	// responsive". That error is fine - we only care that no service
	// commands were invoked.
	_ = Start(p)
	if len(called) > 0 {
		t.Fatalf("Start(p) under go test must not invoke any service-manager commands, got: %v", called)
	}
}

func TestServiceInstanceSuffixResolvesSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is environment-dependent on Windows")
	}

	base := t.TempDir()
	realRoot := filepath.Join(base, "real", "ns-home")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "real"), linkRoot); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}

	realPaths := paths.WithRoot(realRoot)
	aliasPaths := paths.WithRoot(filepath.Join(linkRoot, "ns-home"))

	if got, want := serviceInstanceSuffix(aliasPaths), serviceInstanceSuffix(realPaths); got != want {
		t.Fatalf("serviceInstanceSuffix(alias) = %q, want %q", got, want)
	}
	if got, want := launchdServiceLabel(aliasPaths), launchdServiceLabel(realPaths); got != want {
		t.Fatalf("launchdServiceLabel(alias) = %q, want %q", got, want)
	}
	if got, want := systemdServiceName(aliasPaths), systemdServiceName(realPaths); got != want {
		t.Fatalf("systemdServiceName(alias) = %q, want %q", got, want)
	}
	if got, want := windowsTaskName(aliasPaths), windowsTaskName(realPaths); got != want {
		t.Fatalf("windowsTaskName(alias) = %q, want %q", got, want)
	}
}

func TestServiceDefinitionMatchesRootResolvesSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is environment-dependent on Windows")
	}

	base := t.TempDir()
	realRoot := filepath.Join(base, "real", "ns-home")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "real"), linkRoot); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}

	realPaths := paths.WithRoot(realRoot)
	aliasPaths := paths.WithRoot(filepath.Join(linkRoot, "ns-home"))
	home := t.TempDir()
	windowsArgs := "daemon run --root " + quoteWindowsTaskArg(aliasPaths.Root())
	definitions := map[string][]byte{
		"launchd":  []byte(renderLaunchAgent("/usr/local/bin/no-mistakes", aliasPaths, home)),
		"systemd":  []byte(renderSystemdUnit("/usr/local/bin/no-mistakes", aliasPaths, home)),
		"schtasks": []byte("<Task><Actions><Exec><Arguments>" + xmlEscaped(windowsArgs) + "</Arguments></Exec></Actions></Task>"),
	}

	for name, data := range definitions {
		if !serviceDefinitionMatchesRoot(data, realPaths) {
			t.Fatalf("%s definition with alias root did not match real root", name)
		}
	}
}

func TestServiceInstanceSuffixDistinguishesRelativeRootsAcrossWorkingDirs(t *testing.T) {
	cleanup := stubServiceRuntime(t)
	defer cleanup()

	base := t.TempDir()
	firstWD := filepath.Join(base, "first")
	secondWD := filepath.Join(base, "second")
	for _, dir := range []string{firstWD, secondWD} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	relativePaths := paths.WithRoot(filepath.Join(".", "ns-home"))

	if err := os.Chdir(firstWD); err != nil {
		t.Fatal(err)
	}
	first := serviceInstanceSuffix(relativePaths)

	if err := os.Chdir(secondWD); err != nil {
		t.Fatal(err)
	}
	second := serviceInstanceSuffix(relativePaths)

	if first == second {
		t.Fatalf("serviceInstanceSuffix(relative root) = %q for both working directories", first)
	}
}

func TestServiceInstanceSuffixNormalizesCaseOnWindows(t *testing.T) {
	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	base := filepath.Join(t.TempDir(), "Nm-Home")
	upper := paths.WithRoot(strings.ToUpper(base))
	lower := paths.WithRoot(strings.ToLower(base))

	if got, want := serviceInstanceSuffix(upper), serviceInstanceSuffix(lower); got != want {
		t.Fatalf("serviceInstanceSuffix(upper) = %q, want %q", got, want)
	}
}

// TestStopDoesNotTouchManagedDaemonOwnedByDifferentNMHome is the structural
// regression test for the per-NS_HOME scoping. Before scoping, the launchd
// label / systemd unit / Windows task name were globally unique per user.
// Any `go test ./internal/daemon` in any checkout - including worktrees
// without the testing.Testing() bypass - called TestStopNotRunningIsNoop
// -> Stop(tmpdir-p), which matched the global identifier and tore down the
// user's live LaunchAgent-managed daemon. This test pins in the scoping:
// with serviceManagerBypassed explicitly disabled (simulating worktrees
// without the testing.Testing() guard), Stop(p) for a tmpdir paths.Paths
// must still not invoke any destructive service-manager command against
// artifacts owned by a different NS_HOME.
func TestStopDoesNotTouchManagedDaemonOwnedByDifferentNMHome(t *testing.T) {
	cleanup := stubServiceRuntime(t)
	defer cleanup()

	// Explicitly bypass the testing.Testing() guard. This simulates a
	// test binary compiled from a codebase that predates or lacks the
	// bypass, which is exactly the failure mode observed in pipeline
	// worktrees rebased onto older main branches.
	serviceManagerBypassed = func() (bool, error) { return false, nil }

	home := t.TempDir()
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "99999"}, nil }
	runtimeGOOS = runtime.GOOS

	// Simulate a live managed daemon owned by a DIFFERENT NS_HOME - i.e.
	// the user's real ~/.no-mistakes - by seeding the artifact that an
	// older unscoped binary would have installed (the legacy global name),
	// plus the scoped artifact a modern binary would install for that
	// other NS_HOME. Stop(p) for a test p.Root() must touch neither.
	otherP := paths.WithRoot(filepath.Join(home, "real-ns-home"))
	switch runtime.GOOS {
	case "darwin":
		plistDir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(plistDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plistDir, legacyLaunchdServiceLabel+".plist"), []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plistDir, launchdServiceLabel(otherP)+".plist"), []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "linux":
		unitDir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unitDir, legacySystemdServiceName), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unitDir, systemdServiceName(otherP)), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var called []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		called = append(called, name+" "+strings.Join(args, " "))
		// For Windows: pretend the scoped task for this test p is NOT
		// installed (the test p has its own scoped suffix, different from
		// the "owner" otherP). Any query for the legacy or otherP's task
		// would still succeed, but only the test-p query path reaches
		// serviceCommandRunner inside managedServiceInstalled(p).
		return nil, fmt.Errorf("not found")
	}

	p := paths.WithRoot(filepath.Join(t.TempDir(), "test-ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	if err := Stop(p); err != nil {
		t.Fatalf("Stop(p) should be a no-op when no managed daemon is owned by this NS_HOME: %v", err)
	}
	for _, cmd := range called {
		// Destructive subcommands that would tear down another NS_HOME's daemon.
		for _, forbidden := range []string{"bootout", "/End", "/Delete", "--user stop", "--user disable"} {
			if strings.Contains(cmd, forbidden) {
				t.Fatalf("Stop(p) must not touch managed daemon owned by a different NS_HOME, got destructive command: %q", cmd)
			}
		}
	}
}

func TestWaitForDaemonStartKillsChildOnTimeout(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "20ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "1ms")

	oldHealthCheck := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	defer func() { daemonHealthCheck = oldHealthCheck }()

	oldStartTime := daemonProcessStartTime
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		if pid != 4242 {
			t.Fatalf("daemonProcessStartTime pid = %d, want 4242", pid)
		}
		return startedAt, nil
	}
	defer func() { daemonProcessStartTime = oldStartTime }()

	oldKill := daemonKillPID
	killed := 0
	daemonKillPID = func(pid int) error {
		killed = pid
		return nil
	}
	defer func() { daemonKillPID = oldKill }()

	err := waitForDaemonStart(p, 4242, startedAt)
	if err == nil {
		t.Fatal("waitForDaemonStart should fail when daemon never becomes responsive")
	}
	if killed != 4242 {
		t.Fatalf("waitForDaemonStart should kill pid 4242 on timeout, killed=%d", killed)
	}
}

func TestWaitForDaemonStartReturnsCleanupErrorOnTimeout(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "20ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "1ms")

	oldHealthCheck := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	defer func() { daemonHealthCheck = oldHealthCheck }()

	oldStartTime := daemonProcessStartTime
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		if pid != 4242 {
			t.Fatalf("daemonProcessStartTime pid = %d, want 4242", pid)
		}
		return startedAt, nil
	}
	defer func() { daemonProcessStartTime = oldStartTime }()

	oldKill := daemonKillPID
	daemonKillPID = func(pid int) error {
		if pid != 4242 {
			t.Fatalf("daemonKillPID pid = %d, want 4242", pid)
		}
		return fmt.Errorf("kill failed")
	}
	defer func() { daemonKillPID = oldKill }()

	err := waitForDaemonStart(p, 4242, startedAt)
	if err == nil {
		t.Fatal("waitForDaemonStart should fail when cleanup fails")
	}
	if !strings.Contains(err.Error(), "kill failed") {
		t.Fatalf("waitForDaemonStart error = %v, want cleanup failure", err)
	}
}

func TestWaitForDaemonStartSkipsKillForReusedPID(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "20ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "1ms")

	oldHealthCheck := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	defer func() { daemonHealthCheck = oldHealthCheck }()

	oldStartTime := daemonProcessStartTime
	daemonProcessStartTime = func(pid int) (time.Time, error) {
		if pid != 4242 {
			t.Fatalf("daemonProcessStartTime pid = %d, want 4242", pid)
		}
		return startedAt.Add(time.Second), nil
	}
	defer func() { daemonProcessStartTime = oldStartTime }()

	oldKill := daemonKillPID
	killCalled := false
	daemonKillPID = func(int) error {
		killCalled = true
		return nil
	}
	defer func() { daemonKillPID = oldKill }()

	err := waitForDaemonStart(p, 4242, startedAt)
	if err == nil {
		t.Fatal("waitForDaemonStart should fail when daemon never becomes responsive")
	}
	if killCalled {
		t.Fatal("waitForDaemonStart should not kill when pid start time no longer matches")
	}
}

func TestWaitForDaemonStartDoesNotKillWhenPIDZero(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NM_TEST_DAEMON_START_TIMEOUT", "20ms")
	t.Setenv("NM_TEST_DAEMON_START_POLL_INTERVAL", "1ms")

	oldHealthCheck := daemonHealthCheck
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return false, nil }
	defer func() { daemonHealthCheck = oldHealthCheck }()

	oldKill := daemonKillPID
	killCalled := false
	daemonKillPID = func(int) error {
		killCalled = true
		return nil
	}
	defer func() { daemonKillPID = oldKill }()

	if err := waitForDaemonStart(p, 0, time.Time{}); err == nil {
		t.Fatal("waitForDaemonStart should fail when daemon never becomes responsive")
	}
	if killCalled {
		t.Fatal("waitForDaemonStart should not kill when pid is 0 (managed daemon case)")
	}
}

func renderLaunchAgentWithoutEnvironment(exe string, p *paths.Paths) string {
	values := []string{exe, "daemon", "run", "--root", p.Root()}
	var args strings.Builder
	for _, value := range values {
		args.WriteString("    <string>")
		args.WriteString(xmlEscaped(value))
		args.WriteString("</string>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>WorkingDirectory</key>
  <string>%s</string>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`, xmlEscaped(launchdServiceLabel(p)), args.String(), xmlEscaped(p.Root()), xmlEscaped(p.DaemonBootstrapLog()), xmlEscaped(p.DaemonBootstrapLog()))
}

func stubServiceRuntime(t *testing.T) func() {
	t.Helper()
	oldGOOS := runtimeGOOS
	oldUserHomeDir := serviceUserHomeDir
	oldCurrentUser := serviceCurrentUser
	oldExecutablePath := serviceExecutablePath
	oldCommandRunner := serviceCommandRunner
	oldPrepareManagedDaemonLaunch := prepareManagedDaemonLaunch
	oldInspectManagedDaemonService := inspectManagedDaemonService
	oldHealthCheck := daemonHealthCheck
	oldServiceBypass := serviceManagerBypassed
	serviceManagerBypassed = func() (bool, error) { return false, nil }
	prepareManagedDaemonLaunch = func(*paths.Paths) (managedServiceLaunch, error) {
		return managedServiceLaunch{}, nil
	}
	inspectManagedDaemonService = func(*paths.Paths, managedServiceLaunch) (managedServiceState, error) {
		return managedServiceUnknown, nil
	}
	return func() {
		runtimeGOOS = oldGOOS
		serviceUserHomeDir = oldUserHomeDir
		serviceCurrentUser = oldCurrentUser
		serviceExecutablePath = oldExecutablePath
		serviceCommandRunner = oldCommandRunner
		prepareManagedDaemonLaunch = oldPrepareManagedDaemonLaunch
		inspectManagedDaemonService = oldInspectManagedDaemonService
		daemonHealthCheck = oldHealthCheck
		serviceManagerBypassed = oldServiceBypass
	}
}
