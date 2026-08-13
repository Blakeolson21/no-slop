package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestParseSkipPushOptions(t *testing.T) {
	got, err := parseSkipPushOptions([]string{
		"ci.skip",
		"no-slop.skip=test,lint",
	})
	if err != nil {
		t.Fatalf("parseSkipPushOptions() error = %v", err)
	}
	want := []types.StepName{types.StepTest, types.StepLint}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSkipPushOptions() = %v, want %v", got, want)
	}
}

func TestParseSkipPushOptionsAcceptsLegacyAlias(t *testing.T) {
	got, err := parseSkipPushOptions([]string{"no-mistakes.skip=lint,test"})
	if err != nil {
		t.Fatalf("parseSkipPushOptions() error = %v", err)
	}
	want := []types.StepName{types.StepLint, types.StepTest}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSkipPushOptions() = %v, want %v", got, want)
	}
}

func TestParseSkipPushOptionsRejectsConflictingAliases(t *testing.T) {
	_, err := parseSkipPushOptions([]string{"no-slop.skip=test", "no-mistakes.skip=lint"})
	if err == nil {
		t.Fatal("expected conflicting skip aliases to fail")
	}
}

func TestParseSkipPushOptionsRejectsUnknownStep(t *testing.T) {
	_, err := parseSkipPushOptions([]string{"no-slop.skip=test,deploy"})
	if err == nil {
		t.Fatal("expected unknown step to fail")
	}
}

func TestNormalizeNotifyGatePathResolvesLegacyDotGate(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "repo123.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bare); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	t.Setenv("PWD", ".")

	got, err := normalizeNotifyGatePath(".")
	if err != nil {
		t.Fatalf("normalizeNotifyGatePath: %v", err)
	}
	if got == "." || !filepath.IsAbs(got) {
		t.Fatalf("normalizeNotifyGatePath(.) = %q, want absolute path", got)
	}
	want, err := filepath.EvalSymlinks(bare)
	if err != nil {
		want = bare
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotResolved = got
	}
	if gotResolved != want {
		t.Fatalf("normalizeNotifyGatePath(.) = %q (resolved %q), want %q", got, gotResolved, want)
	}
}

func TestFormatSkipPushOptions(t *testing.T) {
	got := formatSkipPushOptions([]types.StepName{types.StepTest, types.StepLint})
	want := []string{"no-slop.skip=test,lint"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formatSkipPushOptions() = %v, want %v", got, want)
	}
}

func TestIntentPushOptionRoundTrip(t *testing.T) {
	// Multi-line, comma- and colon-bearing intent must survive the
	// line-oriented push-option transport intact.
	intent := "add retry to the uploader\n\nwhy: flaky network, commas, colons: ok"
	opt := formatIntentPushOption(intent)
	if opt == "" {
		t.Fatal("formatIntentPushOption returned empty for a non-empty intent")
	}
	got, err := parseIntentPushOptions([]string{"no-slop.skip=test", opt})
	if err != nil {
		t.Fatalf("parseIntentPushOptions() error = %v", err)
	}
	if got != intent {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, intent)
	}
}

func TestParseIntentPushOptionsAcceptsLegacyAlias(t *testing.T) {
	intent := "legacy invocation parity"
	opt := "no-mistakes.intent=" + base64.StdEncoding.EncodeToString([]byte(intent))
	got, err := parseIntentPushOptions([]string{opt})
	if err != nil {
		t.Fatalf("parseIntentPushOptions() error = %v", err)
	}
	if got != intent {
		t.Fatalf("parseIntentPushOptions() = %q, want %q", got, intent)
	}
}

func TestParseIntentPushOptionsRejectsConflictingAliases(t *testing.T) {
	canonical := formatIntentPushOption("canonical")
	legacy := "no-mistakes.intent=" + base64.StdEncoding.EncodeToString([]byte("legacy"))
	_, err := parseIntentPushOptions([]string{canonical, legacy})
	if err == nil {
		t.Fatal("expected conflicting intent aliases to fail")
	}
}

func TestFormatIntentPushOptionEmpty(t *testing.T) {
	if got := formatIntentPushOption("   "); got != "" {
		t.Fatalf("formatIntentPushOption(blank) = %q, want empty", got)
	}
}

func TestParseIntentPushOptionsNone(t *testing.T) {
	got, err := parseIntentPushOptions([]string{"no-slop.skip=test", "ci.skip"})
	if err != nil {
		t.Fatalf("parseIntentPushOptions() error = %v", err)
	}
	if got != "" {
		t.Fatalf("parseIntentPushOptions(no intent) = %q, want empty", got)
	}
}
