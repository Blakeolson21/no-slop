package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/buildinfo"
)

func TestCollectorConfigUsesDotEnvInDevBuildWhenEnvMissing(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = ""
	buildinfo.TelemetryWebsiteID = ""

	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")
	t.Setenv(umamiHostEnv, "")
	t.Setenv(legacyUmamiHostEnv, "")
	t.Setenv(umamiWebsiteIDEnv, "")
	t.Setenv(legacyUmamiWebsiteIDEnv, "")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "NS_UMAMI_HOST=https://dotenv.example\nNS_UMAMI_WEBSITE_ID=website-from-dotenv\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	if host, err := defaultHostValue(); err != nil || host != "https://dotenv.example" {
		t.Fatalf("defaultHostValue() = %q, %v", host, err)
	}
	if websiteID, err := defaultWebsiteID(); err != nil || websiteID != "website-from-dotenv" {
		t.Fatalf("defaultWebsiteID() = %q, %v", websiteID, err)
	}
}

func TestCollectorConfigUsesLegacyDotEnvAliasesInDevBuildWhenEnvMissing(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = ""
	buildinfo.TelemetryWebsiteID = ""

	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")
	t.Setenv(umamiHostEnv, "")
	t.Setenv(legacyUmamiHostEnv, "")
	t.Setenv(umamiWebsiteIDEnv, "")
	t.Setenv(legacyUmamiWebsiteIDEnv, "")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "NO_MISTAKES_UMAMI_HOST=https://legacy-dotenv.example\nNO_MISTAKES_UMAMI_WEBSITE_ID=legacy-website\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	if host, err := defaultHostValue(); err != nil || host != "https://legacy-dotenv.example" {
		t.Fatalf("defaultHostValue() = %q, %v", host, err)
	}
	if websiteID, err := defaultWebsiteID(); err != nil || websiteID != "legacy-website" {
		t.Fatalf("defaultWebsiteID() = %q, %v", websiteID, err)
	}
}

func TestCollectorConfigPrefersEnvVarsOverDotEnvAndEmbeddedConfig(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevVersion := buildinfo.Version
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.Version = prevVersion
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = "https://embedded.example"
	buildinfo.Version = "v1.2.3"
	buildinfo.TelemetryWebsiteID = "embedded-website"

	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")
	t.Setenv(umamiHostEnv, "https://env.example")
	unsetTestEnv(t, legacyUmamiHostEnv)
	t.Setenv(umamiWebsiteIDEnv, "website-from-env")
	unsetTestEnv(t, legacyUmamiWebsiteIDEnv)

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "NS_UMAMI_HOST=https://dotenv.example\nNS_UMAMI_WEBSITE_ID=website-from-dotenv\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	if host, err := defaultHostValue(); err != nil || host != "https://env.example" {
		t.Fatalf("defaultHostValue() = %q, %v", host, err)
	}
	if websiteID, err := defaultWebsiteID(); err != nil || websiteID != "website-from-env" {
		t.Fatalf("defaultWebsiteID() = %q, %v", websiteID, err)
	}
}

func TestCollectorConfigUsesEmbeddedTelemetryHostAndWebsiteID(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevVersion := buildinfo.Version
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.Version = prevVersion
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = "https://embedded.example"
	buildinfo.Version = "v1.2.3"
	buildinfo.TelemetryWebsiteID = "embedded-website"

	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")
	t.Setenv(umamiHostEnv, "")
	t.Setenv(legacyUmamiHostEnv, "")
	t.Setenv(umamiWebsiteIDEnv, "")
	t.Setenv(legacyUmamiWebsiteIDEnv, "")

	if host, err := defaultHostValue(); err != nil || host != "https://embedded.example" {
		t.Fatalf("defaultHostValue() = %q, %v", host, err)
	}
	if websiteID, err := defaultWebsiteID(); err != nil || websiteID != "embedded-website" {
		t.Fatalf("defaultWebsiteID() = %q, %v", websiteID, err)
	}
}

func TestCollectorConfigUsesSelfHostedHostWhenHostConfigMissing(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevVersion := buildinfo.Version
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.Version = prevVersion
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = ""
	buildinfo.Version = "v1.2.3"
	buildinfo.TelemetryWebsiteID = "embedded-website"

	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")
	t.Setenv(umamiHostEnv, "")
	t.Setenv(legacyUmamiHostEnv, "")
	t.Setenv(umamiWebsiteIDEnv, "")
	t.Setenv(legacyUmamiWebsiteIDEnv, "")

	if host, err := defaultHostValue(); err != nil || host != defaultHost {
		t.Fatalf("defaultHostValue() = %q, %v; want %q", host, err, defaultHost)
	}
}

func TestDefaultDisablesTelemetryWhenEnvIsOff(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryWebsiteID = "embedded-website"

	t.Setenv("NS_TELEMETRY", "off")
	unsetTestEnv(t, legacyTelemetryEnv)
	t.Setenv(umamiWebsiteIDEnv, "website-from-env")
	unsetTestEnv(t, legacyUmamiWebsiteIDEnv)

	sink, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if _, ok := sink.(*Client); ok {
		t.Fatal("Default() should disable telemetry when NS_TELEMETRY=off")
	}
}

func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	value, existed := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func TestValidateDefaultConfigRejectsConflictingTelemetryAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{
			name: "telemetry flag",
			env: map[string]string{
				telemetryEnv:       "off",
				legacyTelemetryEnv: "on",
			},
		},
		{
			name: "host",
			env: map[string]string{
				umamiHostEnv:       "https://canonical.example",
				legacyUmamiHostEnv: "https://legacy.example",
			},
		},
		{
			name: "website id",
			env: map[string]string{
				umamiWebsiteIDEnv:       "canonical-site",
				legacyUmamiWebsiteIDEnv: "legacy-site",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevSink := defaultSink
			defaultSink = nil
			defer func() { defaultSink = prevSink }()

			prevHost := buildinfo.TelemetryHost
			prevWebsiteID := buildinfo.TelemetryWebsiteID
			defer func() {
				buildinfo.TelemetryHost = prevHost
				buildinfo.TelemetryWebsiteID = prevWebsiteID
			}()
			buildinfo.TelemetryHost = "https://embedded.example"
			buildinfo.TelemetryWebsiteID = "embedded-website"

			for _, key := range []string{
				telemetryEnv, legacyTelemetryEnv,
				umamiHostEnv, legacyUmamiHostEnv,
				umamiWebsiteIDEnv, legacyUmamiWebsiteIDEnv,
			} {
				t.Setenv(key, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			if err := ValidateDefaultConfig(); err == nil {
				t.Fatal("ValidateDefaultConfig() should reject conflicting canonical and legacy aliases")
			}
		})
	}
}

func TestValidateDefaultConfigRejectsConflictingDotEnvAliases(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = ""
	buildinfo.TelemetryWebsiteID = ""

	for _, key := range []string{
		telemetryEnv, legacyTelemetryEnv,
		umamiHostEnv, legacyUmamiHostEnv,
		umamiWebsiteIDEnv, legacyUmamiWebsiteIDEnv,
	} {
		t.Setenv(key, "")
	}

	dir := t.TempDir()
	content := strings.Join([]string{
		"NS_UMAMI_HOST=https://canonical-dotenv.example",
		"NO_MISTAKES_UMAMI_HOST=https://legacy-dotenv.example",
		"NS_UMAMI_WEBSITE_ID=canonical-website",
		"NO_MISTAKES_UMAMI_WEBSITE_ID=legacy-website",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	if err := ValidateDefaultConfig(); err == nil {
		t.Fatal("ValidateDefaultConfig() should reject conflicting dotenv aliases")
	}
}

func TestValidateDefaultConfigRejectsEmptyDotEnvAliasConflict(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevHost := buildinfo.TelemetryHost
	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost = prevHost
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryHost = ""
	buildinfo.TelemetryWebsiteID = ""

	for _, key := range []string{
		telemetryEnv, legacyTelemetryEnv,
		umamiHostEnv, legacyUmamiHostEnv,
		umamiWebsiteIDEnv, legacyUmamiWebsiteIDEnv,
	} {
		t.Setenv(key, "")
	}

	dir := t.TempDir()
	content := strings.Join([]string{
		"NS_UMAMI_HOST=",
		"NO_MISTAKES_UMAMI_HOST=https://legacy-dotenv.example",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	if err := ValidateDefaultConfig(); err == nil {
		t.Fatal("ValidateDefaultConfig() should reject empty dotenv alias conflicts")
	}
}

func TestDefaultIgnoresDotEnvOutsideRepo(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()

	prevWebsiteID := buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryWebsiteID = prevWebsiteID
	}()
	buildinfo.TelemetryWebsiteID = ""

	t.Setenv(umamiWebsiteIDEnv, "")
	t.Setenv(legacyUmamiWebsiteIDEnv, "")
	t.Setenv(telemetryEnv, "")
	t.Setenv(legacyTelemetryEnv, "")

	parentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(parentDir, ".env"), []byte("NS_UMAMI_WEBSITE_ID=outside-repo\n"), 0o644); err != nil {
		t.Fatalf("write parent .env: %v", err)
	}

	repoDir := filepath.Join(parentDir, "repo")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	subDir := filepath.Join(repoDir, "nested")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	defer os.Chdir(prevWD)

	sink, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if _, ok := sink.(*Client); ok {
		t.Fatal("Default() should ignore dotenv outside repo")
	}
}

func TestParseDotEnvStripsInlineCommentsFromUnquotedValues(t *testing.T) {
	values := parseDotEnv([]byte("NS_UMAMI_WEBSITE_ID=abc123 # dev\n"))

	if got := values[umamiWebsiteIDEnv]; got != "abc123" {
		t.Fatalf("website ID = %q, want %q", got, "abc123")
	}
}

func TestParseDotEnvPreservesHashesInQuotedValues(t *testing.T) {
	values := parseDotEnv([]byte("NS_UMAMI_WEBSITE_ID=\"abc # dev\"\n"))

	if got := values[umamiWebsiteIDEnv]; got != "abc # dev" {
		t.Fatalf("website ID = %q, want %q", got, "abc # dev")
	}
}
