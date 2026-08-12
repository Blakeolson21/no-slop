package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/precheck"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

func TestLoadGitChangesReturnsBaselineCurrentAndAddedContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	writeFixture(t, dir, "app.go", "package app\n\nconst existing = \"keep\"\n")
	gitRun(t, dir, "add", "app.go")
	gitRun(t, dir, "commit", "-m", "initial")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "switch", "-c", "feature/change")
	writeFixture(t, dir, "app.go", "package app\n\nconst existing = \"keep\"\nconst added = \"new\"\n")
	gitRun(t, dir, "add", "app.go")
	gitRun(t, dir, "commit", "-m", "change")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	change := changes[0]
	if change.Status != risk.Modified || change.Added != 1 || change.Deleted != 0 {
		t.Fatalf("metadata = %+v", change)
	}
	if change.AddedContent != "\n\n\nconst added = \"new\"\n" {
		t.Fatalf("added content = %q", change.AddedContent)
	}
	if change.BaselineContent == change.CurrentContent || change.BaselineContent == "" {
		t.Fatalf("baseline/current were not loaded: %+v", change)
	}
}

func TestLoadGitChangesRecognizesExactRename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	writeFixture(t, dir, "old.go", "package sample\n\nfunc increment(i int) int {\n\t// increment i\n\ti += 1\n\treturn i\n}\n")
	gitRun(t, dir, "add", "old.go")
	gitRun(t, dir, "commit", "-m", "base")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "mv", "old.go", "new.go")
	gitRun(t, dir, "commit", "-m", "rename")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one rename", changes)
	}
	change := changes[0]
	if change.Status != risk.Renamed || change.Path != "new.go" {
		t.Fatalf("change = %+v, want exact rename to new.go", change)
	}
	if change.BaselineContent != change.CurrentContent {
		t.Fatalf("rename content differs: baseline %q current %q", change.BaselineContent, change.CurrentContent)
	}
	if change.AddedContent != "" {
		t.Fatalf("rename added content = %q, want empty", change.AddedContent)
	}
	findings := precheck.Scan([]precheck.File{{
		Path:            change.Path,
		AddedContent:    change.AddedContent,
		BaselineContent: change.BaselineContent,
		CurrentContent:  change.CurrentContent,
	}}, "")
	if len(findings) != 0 {
		t.Fatalf("exact rename produced findings: %+v", findings)
	}
}

func TestLoadGitChangesPreservesModifiedRenameIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	writeFixture(t, dir, "old.go", "package sample\n\nfunc increment(i int) int {\n\t// increment i\n\ti += 1\n\treturn i\n}\n\nfunc value() int { return 1 }\n")
	gitRun(t, dir, "add", "old.go")
	gitRun(t, dir, "commit", "-m", "base")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "mv", "old.go", "new.go")
	writeFixture(t, dir, "new.go", "package sample\n\nfunc increment(i int) int {\n\t// increment i\n\ti += 1\n\treturn i\n}\n\nfunc value() int { return 2 }\n")
	gitRun(t, dir, "add", "new.go")
	gitRun(t, dir, "commit", "-m", "rename and edit")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one modified rename", changes)
	}
	change := changes[0]
	if change.Status != risk.Modified || change.Path != "new.go" || change.Added != 1 || change.Deleted != 1 {
		t.Fatalf("change = %+v, want one-line modified rename", change)
	}
	if strings.Contains(change.AddedContent, "increment i") {
		t.Fatalf("added content rescanned unchanged comment: %q", change.AddedContent)
	}
	if findings := precheck.Scan([]precheck.File{{Path: change.Path, AddedContent: change.AddedContent, BaselineContent: change.BaselineContent, CurrentContent: change.CurrentContent}}, ""); len(findings) != 0 {
		t.Fatalf("modified rename produced findings: %+v", findings)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(bytesTrimSpace(output))
}

func bytesTrimSpace(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
