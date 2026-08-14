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

func TestMakeBuildProducesCanonicalAndLegacyGateBinaries(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	workDir := writeTestMakeWorkspace(t)
	binDir := filepath.Join(workDir, "built-bin")
	runMakeBuild(t, makePath, workDir, binDir, nil)
	for _, name := range []string{"no-slop", "no-mistakes", "noslop"} {
		path := filepath.Join(binDir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing built artifact %s: %v", name, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			t.Fatalf("built artifact %s mode = %s, want executable file", name, info.Mode())
		}
	}
}

func TestMakeDistProducesCanonicalAndLegacyArchives(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	if _, err := exec.LookPath("zip"); err != nil {
		t.Skip("zip not available")
	}
	workDir := writeTestMakeWorkspace(t)
	distDir := filepath.Join(workDir, "release-dist")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, makePath, "dist", "VERSION=v1.2.3", "DIST_DIR="+distDir)
	cmd.Dir = workDir
	cmd.Env = filteredEnv(os.Environ(), "UMAMI_HOST", "UMAMI_WEBSITE_ID", "NS_UMAMI_HOST", "NS_UMAMI_WEBSITE_ID", "NO_MISTAKES_UMAMI_HOST", "NO_MISTAKES_UMAMI_WEBSITE_ID")
	configureMakeTestCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("make dist timed out after %s\n%s", 45*time.Second, output)
	}
	if err != nil {
		t.Fatalf("make dist: %v\n%s", err, output)
	}
	for _, name := range []string{
		"no-slop-v1.2.3-linux-amd64.tar.gz",
		"no-mistakes-v1.2.3-linux-amd64.tar.gz",
		"no-slop-v1.2.3-windows-amd64.zip",
		"no-mistakes-v1.2.3-windows-amd64.zip",
	} {
		info, err := os.Stat(filepath.Join(distDir, name))
		if err != nil {
			t.Fatalf("missing archive %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("archive %s is empty", name)
		}
	}
}

func TestMakeBuildAcceptsLegacyDotEnvTelemetryAlias(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("NO_MISTAKES_UMAMI_HOST=https://legacy.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := runMakeBuildInfo(t, makePath, workDir, nil)
	if !strings.Contains(output, "TelemetryHost=https://legacy.example") {
		t.Fatalf("built binary should accept legacy .env alias:\n%s", output)
	}
}

func TestMakeBuildRejectsConflictingDotEnvTelemetryAliases(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	workDir := writeTestMakeWorkspace(t)
	data := "NS_UMAMI_HOST=https://canonical.example\nNO_MISTAKES_UMAMI_HOST=https://legacy.example\n"
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, makePath, "build", "BIN_DIR="+filepath.Join(workDir, "bin"))
	cmd.Dir = workDir
	cmd.Env = filteredEnv(os.Environ(), "UMAMI_HOST", "UMAMI_WEBSITE_ID", "NS_UMAMI_HOST", "NS_UMAMI_WEBSITE_ID", "NO_MISTAKES_UMAMI_HOST", "NO_MISTAKES_UMAMI_WEBSITE_ID")
	configureMakeTestCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("make build timed out after %s\n%s", 20*time.Second, output)
	}
	if err == nil || !strings.Contains(string(output), "same setting with different values") {
		t.Fatalf("conflicting aliases should fail: %v\n%s", err, output)
	}
}

func TestMakeBuildPrioritizesDotEnvUmamiWebsiteID(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("NS_UMAMI_WEBSITE_ID=website-from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := runMakeBuildInfo(t, makePath, workDir, map[string]string{
		"UMAMI_WEBSITE_ID": "website-from-env",
	})

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("built binary should embed .env website id, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryWebsiteID=website-from-env") {
		t.Fatalf("built binary should not prefer env website id when .env exists, got:\n%s", output)
	}
}

func TestMakeBuildUsesEnvUmamiWebsiteIDWhenDotEnvMissing(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	output := runMakeBuildInfo(t, makePath, workDir, map[string]string{
		"UMAMI_WEBSITE_ID": "website-from-env",
	})

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-env") {
		t.Fatalf("built binary should embed env website id when .env is absent, got:\n%s", output)
	}
}

func TestMakeBuildEmbedsDefaultSelfHostedTelemetryConfig(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	output := runMakeBuildInfo(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryHost=https://a.kunchenguid.com") {
		t.Fatalf("built binary should embed default telemetry host, got:\n%s", output)
	}
	if !strings.Contains(output, "TelemetryWebsiteID=f959e889-92f5-4121-8a1f-571b10861198") {
		t.Fatalf("built binary should embed default telemetry website id, got:\n%s", output)
	}
}

func TestMakeBuildPrioritizesDotEnvUmamiHost(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("NS_UMAMI_HOST=https://dotenv.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := runMakeBuildInfo(t, makePath, workDir, map[string]string{
		"UMAMI_HOST": "https://env.example",
	})

	if !strings.Contains(output, "TelemetryHost=https://dotenv.example") {
		t.Fatalf("built binary should embed .env telemetry host, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryHost=https://env.example") {
		t.Fatalf("built binary should not prefer env telemetry host when .env exists, got:\n%s", output)
	}
}

func TestMakeBuildIgnoresUnrelatedDotEnvEntries(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("VERSION=from-dotenv\nNS_UMAMI_WEBSITE_ID=website-from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := runMakeBuildInfo(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("built binary should still embed dotenv website id, got:\n%s", output)
	}
	if strings.Contains(output, "/internal/buildinfo.Version=from-dotenv") {
		t.Fatalf("make build should ignore unrelated dotenv entries, got:\n%s", output)
	}
}

func TestMakeBuildStripsInlineCommentsFromDotEnvUmamiWebsiteID(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("NS_UMAMI_WEBSITE_ID=website-from-dotenv # dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := runMakeBuildInfo(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("built binary should strip inline comments from dotenv website id, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv # dev") {
		t.Fatalf("make build output should not embed inline comments in website id, got:\n%s", output)
	}
}

func TestMakeBuildPreservesQuotedHashInDotEnvUmamiWebsiteID(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("NS_UMAMI_WEBSITE_ID=\"website # dev\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := runMakeBuildInfo(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website # dev") {
		t.Fatalf("built binary should preserve quoted hashes in dotenv website id, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryWebsiteID=\"website") {
		t.Fatalf("make build output should not truncate quoted dotenv website id, got:\n%s", output)
	}
}

func skipMakeBuildTestsOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("make build tests are POSIX-oriented")
	}
}

func writeTestMakeWorkspace(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Makefile"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeMakeWorkspaceFile(t, workDir, "go.mod", "module github.com/Blakeolson21/no-slop\n\ngo 1.23\n")
	writeMakeWorkspaceFile(t, workDir, filepath.Join("internal", "buildinfo", "buildinfo.go"), `package buildinfo

var Version = "dev"
var Commit = "unknown"
var Date = "unknown"
var TelemetryHost = ""
var TelemetryWebsiteID = ""
`)
	writeMakeWorkspaceFile(t, workDir, filepath.Join("cmd", "no-slop", "main.go"), `package main

import (
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/buildinfo"
)

func main() {
	fmt.Printf("Version=%s\nTelemetryHost=%s\nTelemetryWebsiteID=%s\n", buildinfo.Version, buildinfo.TelemetryHost, buildinfo.TelemetryWebsiteID)
}
`)
	writeMakeWorkspaceFile(t, workDir, filepath.Join("cmd", "no-mistakes", "main.go"), `package main

import "github.com/Blakeolson21/no-slop/internal/buildinfo"

func main() {
	_ = buildinfo.Version
}
`)
	writeMakeWorkspaceFile(t, workDir, filepath.Join("cmd", "noslop", "main.go"), `package main

func main() {}
`)
	return workDir
}

func writeMakeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runMakeBuildInfo(t *testing.T, makePath, workDir string, extraEnv map[string]string) string {
	t.Helper()
	binDir := filepath.Join(workDir, "bin")
	runMakeBuild(t, makePath, workDir, binDir, extraEnv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "no-slop"))
	configureMakeTestCommand(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("built no-slop timed out after %s\n%s", 5*time.Second, out)
	}
	if err != nil {
		t.Fatalf("built no-slop failed: %v\n%s", err, out)
	}
	return string(out)
}

func runMakeBuild(t *testing.T, makePath, workDir, binDir string, extraEnv map[string]string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, makePath, "build", "BIN_DIR="+binDir)
	cmd.Dir = workDir
	cmd.Env = filteredEnv(os.Environ(), "UMAMI_HOST", "UMAMI_WEBSITE_ID", "NS_UMAMI_HOST", "NS_UMAMI_WEBSITE_ID", "NO_MISTAKES_UMAMI_HOST", "NO_MISTAKES_UMAMI_WEBSITE_ID")
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	configureMakeTestCommand(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("make build timed out after %s\n%s", 20*time.Second, out)
	}
	if err != nil {
		t.Fatalf("make build failed: %v\n%s", err, out)
	}
}

func configureMakeTestCommand(cmd *exec.Cmd) {
	shellenv.ConfigureShellCommand(cmd)
}
