package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

func TestInstallScriptInstallsUserOwnedBinaryAndPathSymlink(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	home := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-darwin-arm64.tar.gz")
	binaryScript := "#!/bin/sh\nexit 0\n"
	makeInstallArchive(t, archivePath, binaryScript)
	fakeBin := makeFakeInstallCommands(t)
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}

	runInstallScript(t, home, fakeBin, map[string]string{
		"FAKE_RELEASE_ARCHIVE": archivePath,
	})

	realBin := filepath.Join(home, ".no-mistakes", "bin", "no-slop")
	assertFileContent(t, realBin, binaryScript)
	assertSymlinkTarget(t, filepath.Join(localBin, "no-slop"), realBin)
	assertSymlinkTarget(t, filepath.Join(localBin, "no-mistakes"), realBin)
}

func TestInstallScriptReplacesExistingPathEntryWithSymlink(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	home := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-darwin-arm64.tar.gz")
	binaryScript := "#!/bin/sh\nexit 0\n"
	makeInstallArchive(t, archivePath, binaryScript)
	fakeBin := makeFakeInstallCommands(t)
	linkDir := filepath.Join(t.TempDir(), "link-bin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(linkDir, "no-slop")
	if err := os.WriteFile(oldPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	runInstallScript(t, home, fakeBin, map[string]string{
		"FAKE_RELEASE_ARCHIVE": archivePath,
		"NS_LINK_DIR":          linkDir,
	})

	realBin := filepath.Join(home, ".no-mistakes", "bin", "no-slop")
	assertFileContent(t, realBin, binaryScript)
	assertSymlinkTarget(t, oldPath, realBin)
}

func TestInstallScriptCreatesLegacyAliasWhenLinkDirIsInstallDir(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	home := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-darwin-arm64.tar.gz")
	binaryScript := "#!/bin/sh\nexit 0\n"
	makeInstallArchive(t, archivePath, binaryScript)
	fakeBin := makeFakeInstallCommands(t)
	installDir := filepath.Join(home, ".no-mistakes", "bin")

	runInstallScript(t, home, fakeBin, map[string]string{
		"FAKE_RELEASE_ARCHIVE": archivePath,
		"NS_INSTALL_DIR":       installDir,
		"NS_LINK_DIR":          installDir,
	})

	realBin := filepath.Join(installDir, "no-slop")
	assertFileContent(t, realBin, binaryScript)
	assertSymlinkTarget(t, filepath.Join(installDir, "no-mistakes"), realBin)
}

func TestInstallScriptRestartsDaemonAfterInstall(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	home := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-darwin-arm64.tar.gz")
	callLog := filepath.Join(t.TempDir(), "calls.log")
	makeInstallArchive(t, archivePath, "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"$NS_CALL_LOG\"\n")
	fakeBin := makeFakeInstallCommands(t)
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}

	runInstallScript(t, home, fakeBin, map[string]string{
		"FAKE_RELEASE_ARCHIVE": archivePath,
		"NS_CALL_LOG":          callLog,
	})

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "daemon restart") {
		t.Fatalf("install.sh should restart the daemon after install, got calls %q", string(data))
	}
}

func TestInstallScriptFailsWhenDaemonRestartFails(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	home := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-darwin-arm64.tar.gz")
	callLog := filepath.Join(t.TempDir(), "calls.log")
	makeInstallArchive(t, archivePath, "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"$NS_CALL_LOG\"\nif [ \"$1\" = \"daemon\" ] && [ \"$2\" = \"restart\" ]; then\n  exit 23\nfi\n")
	fakeBin := makeFakeInstallCommands(t)
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := runInstallScriptCommand(t, home, fakeBin, map[string]string{
		"FAKE_RELEASE_ARCHIVE": archivePath,
		"NS_CALL_LOG":          callLog,
	})
	if err == nil {
		t.Fatalf("install.sh should fail when daemon restart fails\n%s", output)
	}

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "daemon restart") {
		t.Fatalf("install.sh should still attempt daemon restart, got calls %q", string(data))
	}
}

func TestInstallScriptRejectsEmptyAliasConflicts(t *testing.T) {
	skipInstallScriptTestsOnWindows(t)

	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{
			name: "install dir",
			env: map[string]string{
				"NS_INSTALL_DIR":          "",
				"NO_MISTAKES_INSTALL_DIR": filepath.Join(t.TempDir(), "legacy-bin"),
			},
		},
		{
			name: "link dir",
			env: map[string]string{
				"NS_LINK_DIR":          "",
				"NO_MISTAKES_LINK_DIR": filepath.Join(t.TempDir(), "legacy-links"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runInstallScriptCommand(t, t.TempDir(), makeFakeInstallCommands(t), tc.env)
			if err == nil || !strings.Contains(string(output), "same setting with different values") {
				t.Fatalf("install.sh should reject empty alias conflict: %v\n%s", err, output)
			}
		})
	}
}

func TestPowerShellInstallScriptRejectsEmptyAliasConflict(t *testing.T) {
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		shell, err = exec.LookPath("powershell")
	}
	if err != nil {
		t.Skip("PowerShell not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-File", filepath.Join("docs", "install.ps1"))
	cmd.Env = append(filteredEnv(os.Environ(), "NS_INSTALL_DIR", "NO_MISTAKES_INSTALL_DIR"), []string{
		"NS_INSTALL_DIR=",
		"NO_MISTAKES_INSTALL_DIR=" + filepath.Join(t.TempDir(), "legacy-bin"),
	}...)
	shellenv.ConfigureShellCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("install.ps1 timed out after %s\n%s", 15*time.Second, output)
	}
	if err == nil || !strings.Contains(string(output), "same setting with different values") {
		t.Fatalf("install.ps1 should reject empty alias conflict: %v\n%s", err, output)
	}
}

func TestPowerShellInstallScriptChecksDaemonRestartFailure(t *testing.T) {
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		shell, err = exec.LookPath("powershell")
	}
	if err != nil {
		t.Skip("PowerShell not available")
	}

	installDir := filepath.Join(t.TempDir(), "install")
	archivePath := filepath.Join(t.TempDir(), "no-slop-v1.2.3-windows-amd64.zip")
	makePowerShellInstallArchive(t, archivePath, "fake binary")

	scriptPath, err := filepath.Abs(filepath.Join("docs", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`
function Invoke-RestMethod {
    param([string]$Uri)
    [pscustomobject]@{ tag_name = 'v1.2.3' }
}
function Invoke-WebRequest {
    param([string]$Uri, [string]$OutFile)
    Copy-Item -LiteralPath $env:NS_FAKE_RELEASE_ARCHIVE -Destination $OutFile -Force
}
function Start-Process {
    param(
        [string]$FilePath,
        [string[]]$ArgumentList,
        [switch]$Wait,
        [switch]$PassThru,
        [switch]$NoNewWindow
    )
    if (-not (Test-Path -LiteralPath $FilePath)) {
        throw "missing installed no-slop: $FilePath"
    }
    if ($FilePath -ne "$env:NS_INSTALL_DIR\no-slop.exe") {
        throw "unexpected restart path: $FilePath"
    }
    if (($ArgumentList -join ' ') -ne 'daemon restart') {
        throw "unexpected restart arguments: $($ArgumentList -join ' ')"
    }
    [pscustomobject]@{ ExitCode = 23 }
}
. %s
`, powerShellSingleQuoted(scriptPath))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Env = append(filteredEnv(os.Environ(), "NS_INSTALL_DIR", "NO_MISTAKES_INSTALL_DIR", "NS_FAKE_RELEASE_ARCHIVE", "PROCESSOR_ARCHITECTURE"), []string{
		"NS_INSTALL_DIR=" + installDir,
		"NS_FAKE_RELEASE_ARCHIVE=" + archivePath,
		"PROCESSOR_ARCHITECTURE=AMD64",
	}...)
	shellenv.ConfigureShellCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("install.ps1 timed out after %s\n%s", 20*time.Second, output)
	}
	if err == nil {
		t.Fatalf("install.ps1 should fail when daemon restart fails\n%s", output)
	}
	if !strings.Contains(string(output), "Failed to restart daemon (exit code 23)") {
		t.Fatalf("install.ps1 should surface restart failure, got: %v\n%s", err, output)
	}
}

func skipInstallScriptTestsOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a POSIX installer; Windows uses install.ps1")
	}
}

func runInstallScript(t *testing.T, home, fakeBin string, extraEnv map[string]string) {
	t.Helper()
	output, err := runInstallScriptCommand(t, home, fakeBin, extraEnv)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
	}
}

func runInstallScriptCommand(t *testing.T, home, fakeBin string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "docs/install.sh")
	pathValue := strings.Join([]string{fakeBin, filepath.Join(home, ".local", "bin"), os.Getenv("PATH")}, string(os.PathListSeparator))
	cmd.Env = append(filteredEnv(os.Environ(), "HOME", "PATH", "NS_INSTALL_DIR", "NO_MISTAKES_INSTALL_DIR", "NS_LINK_DIR", "NO_MISTAKES_LINK_DIR"), []string{
		"HOME=" + home,
		"PATH=" + pathValue,
	}...)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	shellenv.ConfigureShellCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("install.sh timed out after %s", 30*time.Second)
	}
	return output, err
}

func filteredEnv(env []string, excluded ...string) []string {
	blocked := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		blocked[key] = struct{}{}
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			filtered = append(filtered, entry)
			continue
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func makeInstallArchive(t *testing.T, archivePath, binaryContent string) {
	t.Helper()

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	data := []byte(binaryContent)
	hdr := &tar.Header{Name: "no-slop", Mode: 0o755, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func makePowerShellInstallArchive(t *testing.T, archivePath, binaryContent string) {
	t.Helper()

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	hdr := &zip.FileHeader{Name: "no-slop.exe", Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(binaryContent)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func powerShellSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func makeFakeInstallCommands(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "uname"), `#!/bin/sh
case "$1" in
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) command uname "$@" ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -n "$out" ]; then
  cp "$FAKE_RELEASE_ARCHIVE" "$out"
  exit 0
fi
	printf '{"tag_name":"v1.2.3"}'
`)
	writeExecutable(t, filepath.Join(binDir, "sudo"), "#!/bin/sh\nexec \"$@\"\n")
	return binDir
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("file %s = %q, want %q", path, string(data), want)
	}
}

func assertSymlinkTarget(t *testing.T, path, wantTarget string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if target != wantTarget {
		t.Fatalf("symlink %s -> %s, want %s", path, target, wantTarget)
	}
}
