package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "probe@example.com")
	gitIn(t, dir, "config", "user.name", "Probe")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func changeFor(t *testing.T, changes []engine.Change, path string) engine.Change {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			return change
		}
	}
	t.Fatalf("no change loaded for %q in %+v", path, changes)
	return engine.Change{}
}

// TestBaselineSiblingScopeIsDirectoryAndExtensionBound states out loud what the
// collision context actually contains. It was never pinned anywhere, so nobody
// reading the guard could see that a file one directory away, or the same
// directory with a different extension, was invisible to it.
func TestBaselineSiblingScopeIsDirectoryAndExtensionBound(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	writeIn(t, dir, "src/auth.ts", "export const marker = 'same-directory-other-extension'\n")
	writeIn(t, dir, "src/peer.tsx", "export const marker = 'same-directory-same-extension'\n")
	writeIn(t, dir, "lib/other.tsx", "export const marker = 'other-directory'\n")
	writeIn(t, dir, "src/Guard.tsx", "export const Guard = 1\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	writeIn(t, dir, "src/Guard.tsx", "export const Guard = 2\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	scope := changeFor(t, changes, "src/Guard.tsx").BaselineContext
	if !strings.Contains(scope, "same-directory-same-extension") {
		t.Error("sibling context omits a same-directory, same-extension file")
	}
	if strings.Contains(scope, "same-directory-other-extension") {
		t.Error("sibling context unexpectedly includes another extension; update the classifier comment if this changed")
	}
	if strings.Contains(scope, "other-directory") {
		t.Error("sibling context unexpectedly includes another directory; update the classifier comment if this changed")
	}
}

// TestBaselineSiblingScopeIsBounded pins the fan-out ceiling. Each sibling
// costs one `git show`, and an unbounded scope turned one classification in a
// large directory into minutes of subprocess work. Past the cap the context is
// reported truncated, which the classifier reads as "no sound collision answer"
// rather than "no collision".
func TestBaselineSiblingScopeIsBounded(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	for index := 0; index < 260; index++ {
		writeIn(t, dir, fmt.Sprintf("pkg/file%03d.go", index), fmt.Sprintf("package pkg\n\nvar marker%03d = %d\n", index, index))
	}
	writeIn(t, dir, "pkg/target.go", "package pkg\n\nvar target = 1\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	writeIn(t, dir, "pkg/target.go", "package pkg\n\nvar target = 2\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !changeFor(t, changes, "pkg/target.go").BaselineContextTruncated {
		t.Fatal("a 260-sibling directory did not report a truncated collision context")
	}
}

// TestLeakScanContentSurvivesADiffSuppressingGitattribute is the loader half of
// the blinding case. A committed `-diff` attribute makes git emit no hunks, and
// the added content parsed out of those hunks was the only thing the mandatory
// leak scan ever saw.
func TestLeakScanContentSurvivesADiffSuppressingGitattribute(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	writeIn(t, dir, ".gitattributes", "NOTES.md -diff\n")
	writeIn(t, dir, "NOTES.md", "# Notes\n\nnothing here yet\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	writeIn(t, dir, "NOTES.md", "# Notes\n\nAKIAIOSFODNN7EXAMPLE\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	notes := changeFor(t, changes, "NOTES.md")
	if !strings.Contains(notes.AddedContent, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("scannable content is empty, so the leak scan would report clean: %+v", notes)
	}
	if notes.ScanState != engine.ScanWholeBlobFallback {
		t.Fatalf("scan state = %q, want the fallback to be named rather than silent", notes.ScanState)
	}
	if notes.Added == 0 {
		t.Fatal("added count stayed zero, which demotes the tier with no override line")
	}
}

// TestUnreadableEntryIsQuarantinedRatherThanFatal covers a submodule gitlink
// whose target object is absent from the local store. One unreadable entry must
// not stop every other path in the change from being scanned.
func TestUnreadableEntryIsQuarantinedRatherThanFatal(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	writeIn(t, dir, "README.md", "# Project\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	writeIn(t, dir, "README.md", "# Project\n\nPlain update.\n")
	gitIn(t, dir, "add", "-A")
	// A gitlink whose commit object this repository does not have.
	gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,0000000000000000000000000000000000000001,vendor/dep")
	gitIn(t, dir, "commit", "-q", "-m", "head")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatalf("one unreadable entry aborted the whole load: %v", err)
	}
	if readme := changeFor(t, changes, "README.md"); !strings.Contains(readme.CurrentContent, "Plain update.") {
		t.Fatalf("the readable path was not loaded: %+v", readme)
	}
	if quarantined := changeFor(t, changes, "vendor/dep"); quarantined.Unreadable == "" {
		t.Fatalf("the unreadable entry carries no reason: %+v", quarantined)
	}
}
