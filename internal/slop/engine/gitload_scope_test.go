package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
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
	if !strings.Contains(notes.AddedContent, "AKIAIOSFODNN7EXAMPLE") { // noslop:allow-leak
		t.Fatalf("scannable content is empty, so the leak scan would report clean: %+v", notes)
	}
	if notes.ScanState != engine.ScanWholeBlobFallback {
		t.Fatalf("scan state = %q, want the fallback to be named rather than silent", notes.ScanState)
	}
	if notes.Added == 0 {
		t.Fatal("added count stayed zero, which demotes the tier with no override line")
	}
}

// TestUnreadableEntryIsQuarantinedRatherThanFatal covers a blob whose object is
// absent from the local store. One unreadable entry must not stop every other
// path in the change from being scanned.
func TestUnreadableEntryIsQuarantinedRatherThanFatal(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	writeIn(t, dir, "README.md", "# Project\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	writeIn(t, dir, "README.md", "# Project\n\nPlain update.\n")
	writeIn(t, dir, "vendor/dep.txt", "vendored payload\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")
	// Remove the blob from the object store, which is the shape an absent
	// object takes: the tree still names it and nothing can read it.
	blob := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD:vendor/dep.txt"))
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatalf("one unreadable entry aborted the whole load: %v", err)
	}
	if readme := changeFor(t, changes, "README.md"); !strings.Contains(readme.CurrentContent, "Plain update.") {
		t.Fatalf("the readable path was not loaded: %+v", readme)
	}
	if quarantined := changeFor(t, changes, "vendor/dep.txt"); quarantined.Unreadable == "" {
		t.Fatalf("the unreadable entry carries no reason: %+v", quarantined)
	}
}

// TestSubmodulePointerBumpIsNamedNotMisreadAsABrokenBlob is the round-3 S7
// probe. `git show <ref>:<path>` fails on a gitlink however healthy the
// submodule is, so reading it as a blob turned every submodule bump, in every
// repository that has one, into a content-unreadable finding quoting a git
// internal error. The pointer is recorded for what it is instead, and the run
// still scans everything else.
func TestSubmodulePointerBumpIsNamedNotMisreadAsABrokenBlob(t *testing.T) {
	t.Parallel()

	upstream := newRepo(t)
	writeIn(t, upstream, "lib.txt", "one\n")
	gitIn(t, upstream, "add", "-A")
	gitIn(t, upstream, "commit", "-q", "-m", "one")
	first := strings.TrimSpace(gitIn(t, upstream, "rev-parse", "HEAD"))
	writeIn(t, upstream, "lib.txt", "two\n")
	gitIn(t, upstream, "add", "-A")
	gitIn(t, upstream, "commit", "-q", "-m", "two")
	second := strings.TrimSpace(gitIn(t, upstream, "rev-parse", "HEAD"))

	dir := newRepo(t)
	writeIn(t, dir, "README.md", "# Project\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")
	gitIn(t, dir, "-C", "sub", "checkout", "-q", first)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))

	gitIn(t, dir, "-C", "sub", "checkout", "-q", second)
	writeIn(t, dir, "README.md", "# Project\n\nPlain update.\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "bump")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatalf("a healthy submodule bump aborted the load: %v", err)
	}
	pointer := changeFor(t, changes, "sub")
	if pointer.Unreadable != "" {
		t.Fatalf("the gitlink was read as a blob: %q", pointer.Unreadable)
	}
	if pointer.ScanState != engine.ScanSubmodulePointer {
		t.Fatalf("scan state = %q, want the gitlink named as a submodule pointer", pointer.ScanState)
	}
	if pointer.SubmodulePointer.BaselineCommit != first || pointer.SubmodulePointer.HeadCommit != second {
		t.Fatalf("pointer = %+v, want %s -> %s", pointer.SubmodulePointer, first, second)
	}
	if readme := changeFor(t, changes, "README.md"); !strings.Contains(readme.CurrentContent, "Plain update.") {
		t.Fatalf("the rest of the change was not scanned: %+v", readme)
	}

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir: dir, Branch: "probe", DefaultBranch: "main", Files: changes,
		Config: engine.Config{Risk: risk.Config{SingleReviewThreshold: 90, FullReviewThreshold: 99}},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatalf("a submodule bump aborted the run: %v", err)
	}
	named := false
	for _, finding := range result.Findings {
		if finding.Lens == "submodule-pointer-unscanned" && finding.Path == "sub" {
			named = true
			if !strings.Contains(finding.Description, first) || !strings.Contains(finding.Description, second) {
				t.Fatalf("the finding does not carry both commits: %q", finding.Description)
			}
		}
		if finding.Lens == "content-unreadable" {
			t.Fatalf("the gitlink still reports as unreadable content: %q", finding.Description)
		}
	}
	if !named {
		t.Fatalf("findings = %+v, want the submodule named as unscanned content", result.Findings)
	}
	check := mandatoryCheck(t, result, "leak scan")
	if len(check.Unarmed) != 1 || !strings.Contains(check.Unarmed[0], "sub") {
		t.Fatalf("leak scan check = %+v, want the submodule named on the check line", check)
	}

	// T8: the bump raises the tier instead of failing the run outright. Its
	// content is in another repository, so no mechanical check here will ever
	// see it and the honest answer is a reviewer's. Failing unconditionally
	// meant no repository with a submodule could ever get a passing gate, with
	// no route through it but turning the gate off.
	if result.Decision.Tier != risk.TierSingleReview {
		t.Fatalf("tier = %q, want the unscannable pointer to raise the tier", result.Decision.Tier)
	}
	raised := false
	for _, escalation := range result.Decision.Escalations {
		if strings.Contains(escalation, "content this run cannot scan") && strings.Contains(escalation, "sub") {
			raised = true
		}
	}
	if !raised {
		t.Fatalf("escalations = %v, want the raise explained", result.Decision.Escalations)
	}
	for _, finding := range result.Findings {
		if finding.Lens == "submodule-pointer-unscanned" && finding.Blocks() {
			t.Fatalf("the pointer bump still fails the run by itself: %+v", finding)
		}
	}
}

// TestAnUnreadableBlobStillBlocks is the other side of T8. Only the submodule
// pointer became non-blocking, and only because its content genuinely lives
// somewhere this gate cannot reach. A path this repository holds and cannot
// read is a broken repository and still fails.
func TestAnUnreadableBlobStillBlocks(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir: t.TempDir(), Branch: "probe", DefaultBranch: "main",
		Files: []engine.Change{{
			Path:       "vendor/blob.bin",
			Status:     risk.Modified,
			Unreadable: "head blob for \"vendor/blob.bin\" could not be read: object missing",
		}},
		Config: engine.Config{Risk: risk.Config{SingleReviewThreshold: 90, FullReviewThreshold: 99}},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatalf("an unreadable blob passed: %+v", result.Findings)
	}
}

// TestAddingABinaryFileIsScannedNotBlocked pins the proportion of the R2 fix
// as the round-3 review reshaped it. Every commit that adds an image must not
// fail, because a mandatory check that blocks ordinary work is a mandatory
// check somebody turns off. But the blob is READ, not skipped: naming it as
// unscanned was honest and still let one NUL byte carry a live credential past
// the scan at exit 0. So the image passes, and the leak-scan line reports the
// reduced coverage rather than a check that never looked.
func TestAddingABinaryFileIsScannedNotBlocked(t *testing.T) {
	t.Parallel()

	dir := newRepo(t)
	writeIn(t, dir, "README.md", "# Project\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")

	changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if state := changeFor(t, changes, "logo.png").ScanState; state != engine.ScanBinarySafe {
		t.Fatalf("scan state = %q, want the binary path read through the binary-safe path", state)
	}

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir: dir, Branch: "probe", DefaultBranch: "main", Files: changes,
		Config: engine.Config{Risk: risk.Config{SingleReviewThreshold: 90, FullReviewThreshold: 99}},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("adding an image failed the gate: %+v", result.Findings)
	}
	check := mandatoryCheck(t, result, "leak scan")
	if len(check.Unarmed) != 0 {
		t.Fatalf("leak scan check = %+v, want no path reported as unscanned", check)
	}
	if len(check.Degraded) != 1 || !strings.Contains(check.Degraded[0], "logo.png") {
		t.Fatalf("leak scan check = %+v, want the binary path named as reduced coverage", check)
	}
}

// TestOneNulByteDoesNotBuyAPass is the round-3 S4 probe. Prepending a single
// NUL to a plain text file makes git call the blob binary, which used to skip
// the mandatory leak scan entirely and pass the run at exit 0 over a live AWS
// key with only a note on the check line.
func TestOneNulByteDoesNotBuyAPass(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		content []byte
	}{
		{name: "one leading NUL", content: append([]byte{0}, []byte("notes\nAKIAIOSFODNN7EXAMPLE\n")...)},
		{name: "NUL between the lines", content: []byte("notes\n\x00AKIAIOSFODNN7EXAMPLE\n")},
		{name: "utf-16 style interleaved NULs", content: utf16ish("notes\nAKIAIOSFODNN7EXAMPLE\n")},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			dir := newRepo(t)
			writeIn(t, dir, "NOTES.md", "notes\n")
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "-m", "base")
			base := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), probe.content, 0o644); err != nil {
				t.Fatal(err)
			}
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "-m", "head")

			changes, err := engine.LoadGitChanges(context.Background(), dir, base, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(context.Background(), engine.Input{
				WorkDir: dir, Branch: "probe", DefaultBranch: "main", Files: changes,
			}, engine.Dependencies{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Passed {
				t.Fatalf("a NUL byte carried a credential past the mandatory scan: %+v", result.MandatoryChecks)
			}
			found := false
			for _, finding := range result.Findings {
				if finding.Lens == "leak-identity-scan" && finding.Path == "NOTES.md" {
					found = true
				}
			}
			if !found {
				t.Fatalf("findings = %+v, want the credential named", result.Findings)
			}
		})
	}
}

// utf16ish interleaves NUL bytes the way UTF-16LE text does, which the
// control-bytes-to-spaces rendering alone would space into nonsense.
func utf16ish(text string) []byte {
	out := make([]byte, 0, len(text)*2)
	for index := 0; index < len(text); index++ {
		out = append(out, text[index], 0)
	}
	return out
}
