package main

import (
	"archive/tar"
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

func TestPowerShellInstallScriptChecksDaemonRestartFailure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("docs", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := parsePowerShellRestartModel(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if model.Assignment != "restart" {
		t.Fatalf("restart process assignment = %q, want restart", model.Assignment)
	}
	if model.Command != "Start-Process" {
		t.Fatalf("restart command = %q, want Start-Process", model.Command)
	}
	if model.Named["FilePath"] != "$installDir\\no-slop.exe" {
		t.Fatalf("restart FilePath = %q, want installed no-slop.exe", model.Named["FilePath"])
	}
	if got, want := strings.Join(model.ArgumentList, " "), "daemon restart"; got != want {
		t.Fatalf("restart arguments = %q, want %q", got, want)
	}
	for _, flag := range []string{"Wait", "PassThru"} {
		if !model.Switches[flag] {
			t.Fatalf("restart command missing -%s", flag)
		}
	}
	if model.ExitCheckVariable != "restart" || model.ExitCheckOperator != "-ne" || model.ExitCheckValue != "0" {
		t.Fatalf("exit check = %s %s %s, want restart -ne 0", model.ExitCheckVariable, model.ExitCheckOperator, model.ExitCheckValue)
	}
	if !strings.Contains(model.ThrowExpression, "$($restart.ExitCode)") {
		t.Fatalf("failure throw should surface restart exit code, got %q", model.ThrowExpression)
	}
}

type powerShellRestartModel struct {
	Assignment        string
	Command           string
	Named             map[string]string
	Switches          map[string]bool
	ArgumentList      []string
	ExitCheckVariable string
	ExitCheckOperator string
	ExitCheckValue    string
	ThrowExpression   string
}

func parsePowerShellRestartModel(script string) (powerShellRestartModel, error) {
	model := powerShellRestartModel{Named: map[string]string{}, Switches: map[string]bool{}}
	lines := strings.Split(script, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "$") && strings.Contains(line, "= Start-Process ") {
			left, right, _ := strings.Cut(line, "=")
			model.Assignment = strings.TrimPrefix(strings.TrimSpace(left), "$")
			commandLines := []string{strings.TrimSpace(right)}
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(next, "if ") {
					break
				}
				commandLines = append(commandLines, next)
				i++
			}
			if err := parsePowerShellCommand(strings.Join(commandLines, " "), &model); err != nil {
				return model, err
			}
			continue
		}
		if strings.HasPrefix(line, "if ") && model.Command != "" {
			if err := parsePowerShellExitCheck(line, &model); err != nil {
				return model, err
			}
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				i++
				if next == "}" {
					break
				}
				if throw, ok := strings.CutPrefix(next, "throw "); ok {
					model.ThrowExpression = strings.Trim(throw, `"`)
				}
			}
		}
	}
	if model.Command == "" {
		return model, fmt.Errorf("restart command not found")
	}
	if model.ExitCheckVariable == "" {
		return model, fmt.Errorf("restart exit check not found")
	}
	if model.ThrowExpression == "" {
		return model, fmt.Errorf("restart failure throw not found")
	}
	return model, nil
}

func parsePowerShellCommand(command string, model *powerShellRestartModel) error {
	tokens := powerShellTokens(command)
	if len(tokens) == 0 {
		return fmt.Errorf("empty command")
	}
	model.Command = tokens[0]
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		if !strings.HasPrefix(token, "-") {
			continue
		}
		name := strings.TrimPrefix(token, "-")
		if name == "ArgumentList" {
			if i+1 >= len(tokens) || tokens[i+1] != "@(" {
				return fmt.Errorf("ArgumentList is not an array literal")
			}
			i += 2
			for ; i < len(tokens) && tokens[i] != ")"; i++ {
				if tokens[i] != "," {
					model.ArgumentList = append(model.ArgumentList, tokens[i])
				}
			}
			continue
		}
		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			model.Named[name] = tokens[i+1]
			i++
			continue
		}
		model.Switches[name] = true
	}
	return nil
}

func parsePowerShellExitCheck(line string, model *powerShellRestartModel) error {
	condition := strings.TrimSpace(strings.TrimPrefix(line, "if "))
	condition = strings.TrimSuffix(condition, "{")
	condition = strings.TrimSpace(condition)
	condition = strings.TrimPrefix(condition, "(")
	condition = strings.TrimSuffix(condition, ")")
	fields := strings.Fields(condition)
	if len(fields) != 3 {
		return fmt.Errorf("unsupported exit check %q", line)
	}
	variable, ok := strings.CutSuffix(strings.TrimPrefix(fields[0], "$"), ".ExitCode")
	if !ok {
		return fmt.Errorf("exit check does not inspect ExitCode: %q", line)
	}
	model.ExitCheckVariable = variable
	model.ExitCheckOperator = fields[1]
	model.ExitCheckValue = fields[2]
	return nil
}

func powerShellTokens(s string) []string {
	var tokens []string
	for i := 0; i < len(s); {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n':
			i++
		case s[i] == '@' && i+1 < len(s) && s[i+1] == '(':
			tokens = append(tokens, "@(")
			i += 2
		case s[i] == ')' || s[i] == ',':
			tokens = append(tokens, string(s[i]))
			i++
		case s[i] == '"' || s[i] == '\'':
			quote := s[i]
			i++
			start := i
			for i < len(s) && s[i] != quote {
				i++
			}
			tokens = append(tokens, s[start:i])
			if i < len(s) {
				i++
			}
		default:
			start := i
			for i < len(s) && !strings.ContainsRune(" \t\r\n),", rune(s[i])) {
				i++
			}
			tokens = append(tokens, s[start:i])
		}
	}
	return tokens
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
	cmd.Env = append(filteredEnv(os.Environ(), "HOME", "PATH"), []string{
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
