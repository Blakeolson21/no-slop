package daemon

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestStartInstallsWindowsTaskAndStartsManagedDaemon(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "nm home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"
	exe := `C:\Program Files\no-slop\no-slop.exe`
	serviceExecutablePath = func() (string, error) { return exe, nil }

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		// Simulate fresh-install: the legacy unsuffixed task is absent, so
		// the pre-install cleanup query fails and cleanupLegacyWindowsTask
		// returns without issuing End/Delete.
		if name == "schtasks" && len(args) >= 4 && args[0] == "/Query" && args[2] == legacyWindowsTaskName && args[3] == "/XML" {
			return nil, fmt.Errorf("task not found")
		}
		return nil, nil
	}
	checks := 0
	daemonHealthCheck = func(*paths.Paths) (bool, error) {
		checks++
		return checks >= 2, nil
	}

	if err := Start(p); err != nil {
		t.Fatal(err)
	}

	wantTaskCommand := strconv.Quote(exe) + " daemon run --root " + strconv.Quote(p.Root())
	wantQueryScopedLegacy := "schtasks /Query /TN " + legacyScopedWindowsTaskName(p) + " /XML"
	wantQueryLegacy := "schtasks /Query /TN " + legacyWindowsTaskName + " /XML"
	wantCreate := "schtasks /Create /TN " + windowsTaskName(p) +
		" /SC ONLOGON /RL LIMITED /F /TR " + wantTaskCommand
	wantRun := "schtasks /Run /TN " + windowsTaskName(p)
	if len(commands) != 4 {
		t.Fatalf("expected schtasks create, both legacy queries, and run, got %v", commands)
	}
	if commands[0] != wantCreate {
		t.Fatalf("create command = %q, want %q", commands[0], wantCreate)
	}
	if commands[1] != wantQueryScopedLegacy || commands[2] != wantQueryLegacy {
		t.Fatalf("legacy query commands = %q and %q, want %q and %q", commands[1], commands[2], wantQueryScopedLegacy, wantQueryLegacy)
	}
	if commands[3] != wantRun {
		t.Fatalf("run command = %q, want %q", commands[3], wantRun)
	}
}

func TestInstallWindowsTaskDoesNotRemoveLegacyTaskForDifferentRoot(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "schtasks" && len(args) >= 4 && args[0] == "/Query" && args[2] == legacyWindowsTaskName && args[3] == "/XML" {
			otherRoot := filepath.Join(t.TempDir(), "other-ns-home")
			return []byte(`<Task><Exec><Command>C:\nm.exe</Command><Arguments>daemon run --root ` + otherRoot + `</Arguments></Exec></Task>`), nil
		}
		return nil, nil
	}

	if err := installWindowsTask(p, `C:\Program Files\no-slop\no-slop.exe`); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 {
		t.Fatalf("install should not end or delete unrelated legacy task, got commands %v", commands)
	}
	if commands[1] != "schtasks /Query /TN "+legacyScopedWindowsTaskName(p)+" /XML" ||
		commands[2] != "schtasks /Query /TN "+legacyWindowsTaskName+" /XML" {
		t.Fatalf("legacy query commands = %v", commands[1:])
	}
}

func TestInstallWindowsTaskReportsLegacyInspectionFailure(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		if name == "schtasks" && len(args) >= 4 && args[0] == "/Query" && args[2] == legacyWindowsTaskName && args[3] == "/XML" {
			return []byte("ERROR: Access is denied."), fmt.Errorf("access denied")
		}
		return nil, nil
	}

	err := installWindowsTask(p, `C:\Program Files\no-slop\no-slop.exe`)
	if err == nil || !strings.Contains(err.Error(), "inspect legacy windows task") {
		t.Fatalf("install error = %v, want legacy inspection failure", err)
	}
}

func TestInstallWindowsTaskKeepsLegacyTaskOnCreateFailure(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "nm home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	var commands []string
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if name == "schtasks" && len(args) > 0 && args[0] == "/Create" {
			return nil, fmt.Errorf("create failed")
		}
		return nil, nil
	}

	err := installWindowsTask(p, `C:\Program Files\no-slop\no-slop.exe`)
	if err == nil {
		t.Fatal("installWindowsTask should fail when schtasks create fails")
	}
	for _, command := range commands {
		if strings.Contains(command, "/End /TN "+legacyWindowsTaskName) || strings.Contains(command, "/Delete /TN "+legacyWindowsTaskName+" /F") {
			t.Fatalf("legacy cleanup should not run before successful scoped install, got %q", command)
		}
	}
}

func TestWindowsManagedDaemonStateUsesRunGeneration(t *testing.T) {
	p := paths.WithRoot(filepath.Join(t.TempDir(), "ns-home"))
	cleanup := stubServiceRuntime(t)
	defer cleanup()
	runtimeGOOS = "windows"

	var command string
	output := "3|100|0\r\n"
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command = name + " " + strings.Join(args, " ")
		return []byte(output), nil
	}

	launch, err := managedDaemonLaunch(p)
	if err != nil {
		t.Fatal(err)
	}
	if launch.windowsRunGeneration != "100" {
		t.Fatalf("launch generation = %q, want 100", launch.windowsRunGeneration)
	}
	state, err := managedDaemonServiceState(p, launch)
	if err != nil {
		t.Fatal(err)
	}
	if state != managedServiceUnknown {
		t.Fatalf("unchanged Ready generation state = %v, want unknown", state)
	}

	output = "3|101|0\r\n"
	state, err = managedDaemonServiceState(p, launch)
	if err != nil {
		t.Fatal(err)
	}
	if state != managedServiceExited {
		t.Fatalf("completed new generation state = %v, want exited", state)
	}
	if !strings.HasPrefix(command, "powershell.exe -NoLogo -NoProfile -NonInteractive -Command ") {
		t.Fatalf("unexpected task state command: %q", command)
	}
	if strings.Contains(command, "/FO LIST") {
		t.Fatalf("task state command uses localized schtasks output: %q", command)
	}
}
