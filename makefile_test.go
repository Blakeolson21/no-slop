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
)

func TestMakeBuildProducesCanonicalAndLegacyGateBinaries(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	output := runMakeDryBuild(t, makePath, writeTestMakeWorkspace(t), nil)
	if !strings.Contains(output, "-o bin/no-slop ./cmd/no-slop") {
		t.Fatalf("make build output missing canonical no-slop binary:\n%s", output)
	}
	if !strings.Contains(output, "-o bin/no-mistakes ./cmd/no-slop") {
		t.Fatalf("make build output missing no-mistakes compatibility binary:\n%s", output)
	}
}

func TestMakeDistProducesCanonicalAndLegacyArchives(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}
	cmd := exec.Command(makePath, "-n", "dist")
	cmd.Dir = writeTestMakeWorkspace(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n dist: %v\n%s", err, output)
	}
	for _, want := range []string{"no-slop-", "no-mistakes-", "cp \"$out\""} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("make dist output missing %q:\n%s", want, output)
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
	output := runMakeDryBuild(t, makePath, workDir, nil)
	if !strings.Contains(output, "TelemetryHost=https://legacy.example") {
		t.Fatalf("make build output should accept legacy .env alias:\n%s", output)
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
	cmd := exec.Command(makePath, "-n", "build")
	cmd.Dir = workDir
	cmd.Env = filteredEnv(os.Environ(), "UMAMI_HOST", "UMAMI_WEBSITE_ID", "NS_UMAMI_HOST", "NS_UMAMI_WEBSITE_ID", "NO_MISTAKES_UMAMI_HOST", "NO_MISTAKES_UMAMI_WEBSITE_ID")
	output, err := cmd.CombinedOutput()
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

	output := runMakeDryBuild(t, makePath, workDir, map[string]string{
		"UMAMI_WEBSITE_ID": "website-from-env",
	})

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("make build output should embed .env website id, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryWebsiteID=website-from-env") {
		t.Fatalf("make build output should not prefer env website id when .env exists, got:\n%s", output)
	}
}

func TestMakeBuildUsesEnvUmamiWebsiteIDWhenDotEnvMissing(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	output := runMakeDryBuild(t, makePath, workDir, map[string]string{
		"UMAMI_WEBSITE_ID": "website-from-env",
	})

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-env") {
		t.Fatalf("make build output should embed env website id when .env is absent, got:\n%s", output)
	}
}

func TestMakeBuildEmbedsDefaultSelfHostedTelemetryConfig(t *testing.T) {
	skipMakeBuildTestsOnWindows(t)

	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not available")
	}

	workDir := writeTestMakeWorkspace(t)
	output := runMakeDryBuild(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryHost=https://a.kunchenguid.com") {
		t.Fatalf("make build output should embed default telemetry host, got:\n%s", output)
	}
	if !strings.Contains(output, "TelemetryWebsiteID=f959e889-92f5-4121-8a1f-571b10861198") {
		t.Fatalf("make build output should embed default telemetry website id, got:\n%s", output)
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

	output := runMakeDryBuild(t, makePath, workDir, map[string]string{
		"UMAMI_HOST": "https://env.example",
	})

	if !strings.Contains(output, "TelemetryHost=https://dotenv.example") {
		t.Fatalf("make build output should embed .env telemetry host, got:\n%s", output)
	}
	if strings.Contains(output, "TelemetryHost=https://env.example") {
		t.Fatalf("make build output should not prefer env telemetry host when .env exists, got:\n%s", output)
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

	output := runMakeDryBuild(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("make build output should still embed dotenv website id, got:\n%s", output)
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

	output := runMakeDryBuild(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website-from-dotenv") {
		t.Fatalf("make build output should strip inline comments from dotenv website id, got:\n%s", output)
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

	output := runMakeDryBuild(t, makePath, workDir, nil)

	if !strings.Contains(output, "TelemetryWebsiteID=website # dev") {
		t.Fatalf("make build output should preserve quoted hashes in dotenv website id, got:\n%s", output)
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
	return workDir
}

func runMakeDryBuild(t *testing.T, makePath, workDir string, extraEnv map[string]string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, makePath, "-n", "build")
	cmd.Dir = workDir
	cmd.Env = filteredEnv(os.Environ(), "UMAMI_HOST", "UMAMI_WEBSITE_ID", "NS_UMAMI_HOST", "NS_UMAMI_WEBSITE_ID", "NO_MISTAKES_UMAMI_HOST", "NO_MISTAKES_UMAMI_WEBSITE_ID")
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n build failed: %v\n%s", err, out)
	}
	return string(out)
}
