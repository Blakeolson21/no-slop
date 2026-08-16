package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/slop/leakscan"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

const (
	renameDetection = "--find-renames"
)

// LoadGitChanges materializes the committed change between two refs. Git
// invocations inherit the repository's bounded subprocess policy.
func LoadGitChanges(ctx context.Context, workDir, baseRef, headRef string) ([]Change, error) {
	if baseRef == "" || headRef == "" {
		return nil, fmt.Errorf("load git changes: base and head refs are required")
	}
	statusOutput, err := git.Output(ctx, workDir, "diff", "--name-status", "-z", renameDetection, baseRef, headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("load changed paths: %w", err)
	}
	gitlinks, err := loadGitlinkPointers(ctx, workDir, baseRef, headRef)
	if err != nil {
		return nil, err
	}
	entries := splitNUL(statusOutput)
	changes := make([]Change, 0, len(entries)/2)
	for index := 0; index < len(entries); {
		rawStatus := entries[index]
		index++
		if index >= len(entries) {
			return nil, fmt.Errorf("load changed paths: unexpected git name-status output")
		}
		baselinePath := entries[index]
		path := baselinePath
		index++
		status := parseChangeStatus(rawStatus)
		isRename := strings.HasPrefix(rawStatus, "R")
		if isRename {
			if index >= len(entries) {
				return nil, fmt.Errorf("load changed paths: rename is missing its destination")
			}
			path = entries[index]
			index++
			if rawStatus != "R100" {
				status = risk.Modified
			}
		}
		change := Change{Path: path, Status: status}
		if isRename {
			change.BaselinePath = baselinePath
		}
		if pointer, gitlink := gitlinks[path]; gitlink {
			// A submodule entry is a gitlink, not a blob, so `git show ref:path`
			// fails on it however healthy the submodule is. Reading it as a blob
			// turned every submodule pointer bump into a content-unreadable
			// finding naming a git internal error, which made the gate unusable
			// in any repository that has one. The pointer is recorded for what
			// it is: content this run could not see, named with the SHAs it
			// moved between, and every other path still scanned.
			change.ScanState = ScanSubmodulePointer
			change.SubmodulePointer = pointer
			changes = append(changes, change)
			continue
		}
		if status != risk.Added {
			change.BaselineContent, err = showGitFile(ctx, workDir, baseRef, baselinePath)
			if err != nil {
				changes = append(changes, quarantine(change, fmt.Sprintf("baseline blob for %q could not be read: %v", baselinePath, err)))
				continue
			}
		}
		if status == risk.Modified {
			change.BaselineContext, change.BaselineContextTruncated, err = loadBaselineSiblingContent(ctx, workDir, baseRef, baselinePath)
			if err != nil {
				return nil, fmt.Errorf("load baseline sibling context for %q: %w", path, err)
			}
		}
		if status != risk.Deleted {
			change.CurrentContent, err = showGitFile(ctx, workDir, headRef, path)
			if err != nil {
				changes = append(changes, quarantine(change, fmt.Sprintf("head blob for %q could not be read: %v", path, err)))
				continue
			}
		}
		content, err := loadAddedContent(ctx, workDir, baseRef, headRef, baselinePath, path, isRename, renameDetection)
		if err != nil {
			return nil, err
		}
		change.AddedContent, change.Added, change.Deleted = content.text, content.added, content.deleted
		applyScanFallback(&change, content.sawHunk)
		changes = append(changes, change)
	}
	return changes, nil
}

// gitlinkMode is git's file mode for a submodule entry.
const gitlinkMode = "160000"

// loadGitlinkPointers maps each submodule path in the diff to the pointer move
// it carries. `git diff --raw` is the one output that names the entry's mode,
// which is the only reliable way to tell a gitlink from a blob before trying to
// read it.
func loadGitlinkPointers(ctx context.Context, workDir, baseRef, headRef string) (map[string]SubmodulePointer, error) {
	raw, err := git.Output(ctx, workDir, "diff", "--raw", "-z", "--abbrev=40", renameDetection, baseRef, headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("load changed entry modes: %w", err)
	}
	pointers := make(map[string]SubmodulePointer)
	fields := splitNUL(raw)
	for index := 0; index < len(fields); {
		header := fields[index]
		index++
		if !strings.HasPrefix(header, ":") {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(header, ":"))
		if len(parts) < 5 || index >= len(fields) {
			continue
		}
		baselineMode, headMode := parts[0], parts[1]
		baselineSHA, headSHA := parts[2], parts[3]
		status := parts[4]
		path := fields[index]
		index++
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(fields) {
				continue
			}
			path = fields[index]
			index++
		}
		if baselineMode != gitlinkMode && headMode != gitlinkMode {
			continue
		}
		pointers[path] = SubmodulePointer{
			BaselineCommit: namedCommit(baselineSHA),
			HeadCommit:     namedCommit(headSHA),
		}
	}
	return pointers, nil
}

// namedCommit renders a raw-diff object id. Git writes all zeroes for a side
// that does not exist, and a commit id genuinely may begin with zeroes, so the
// absent case is recognised by being entirely zeroes rather than by trimming.
func namedCommit(sha string) string {
	if strings.Trim(sha, "0") == "" {
		return "(absent)"
	}
	return sha
}

// quarantine records a path whose content this run could not read, without
// aborting the whole gate. One unreadable entry (a submodule gitlink whose
// object is absent from the local store is the realistic case) must not stop
// every other path in the change from being scanned; the entry carries its own
// reason and the engine turns it into a finding, so the run still fails and
// still says why.
func quarantine(change Change, reason string) Change {
	change.Unreadable = reason
	change.BaselineContent = ""
	change.CurrentContent = ""
	change.AddedContent = ""
	return change
}

// applyScanFallback keeps the mandatory leak scan honest when git rendered no
// diff for a path whose content demonstrably changed.
//
// AddedContent is parsed out of `git diff --unified=0` hunk text, and git
// produces no hunks for anything it treats as binary. A committed
// `.gitattributes` line unsetting the `diff` attribute is enough to reach that
// state on a plain text file, which fed the scanner an empty string, zeroed the
// added and deleted counts the classifier scores, and printed "leak scan
// completed (0 findings)" over a live credential. A check whose blinding is
// indistinguishable from a clean result is worse than no check.
//
// So a text blob whose diff could not be derived is scanned whole. That
// over-reports on that one path, which is the correct direction, and the run
// says it happened. A blob git calls binary is scanned too, through leakscan's
// binary-safe renderings: skipping it was the cheaper bypass, because one NUL
// byte prepended to a plain text file reaches this branch and moved a live
// credential out of a mandatory check's reach at exit 0.
func applyScanFallback(change *Change, sawHunk bool) {
	if change.Status == risk.Deleted || sawHunk || change.BaselineContent == change.CurrentContent {
		return
	}
	// leakscan owns the binary decision, over the whole blob rather than git's
	// 8000-byte sniff window. Mirroring git's window here is what let a NUL past
	// offset 8000 read as text on both sides at once.
	if leakscan.IsBinaryContent(change.CurrentContent) {
		change.ScanState = ScanBinarySafe
		return
	}
	change.ScanState = ScanWholeBlobFallback
	change.AddedContent = change.CurrentContent
	change.Added = countLines(change.CurrentContent)
	change.Deleted = countLines(change.BaselineContent)
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

// maxBaselineSiblings bounds the collision oracle's per-file fan-out. Each
// sibling costs one `git show`, so a directory holding thousands of files
// turned one classification into minutes of subprocess work. Past the cap the
// context is reported truncated rather than silently partial, and the
// classifier refuses to call a change mechanical on a partial view.
const maxBaselineSiblings = 200

func loadBaselineSiblingContent(ctx context.Context, workDir, baseRef, path string) (string, bool, error) {
	dir := filepath.ToSlash(filepath.Dir(path))
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "", false, nil
	}
	scope := dir
	if scope == "." {
		scope = ":(top)"
	}
	output, err := git.Run(ctx, workDir, "ls-tree", "-r", "--name-only", "-z", baseRef, "--", scope)
	if err != nil {
		return "", false, err
	}
	var context strings.Builder
	loaded := 0
	for _, sibling := range splitNUL(output) {
		if sibling == path || filepath.ToSlash(filepath.Dir(sibling)) != dir || strings.ToLower(filepath.Ext(sibling)) != ext {
			continue
		}
		if loaded >= maxBaselineSiblings {
			return context.String(), true, nil
		}
		content, err := showGitFile(ctx, workDir, baseRef, sibling)
		if err != nil {
			return context.String(), true, nil
		}
		context.WriteString(content)
		context.WriteByte('\n')
		loaded++
	}
	return context.String(), false, nil
}

func splitNUL(output string) []string {
	output = strings.TrimSuffix(output, "\x00")
	if output == "" {
		return nil
	}
	return strings.Split(output, "\x00")
}

func parseChangeStatus(raw string) risk.ChangeStatus {
	switch {
	case raw == "A":
		return risk.Added
	case raw == "D":
		return risk.Deleted
	case strings.HasPrefix(raw, "R"):
		return risk.Renamed
	default:
		return risk.Modified
	}
}

func showGitFile(ctx context.Context, workDir, ref, path string) (string, error) {
	return git.Output(ctx, workDir, "show", ref+":"+path)
}

type addedContent struct {
	text    string
	added   int
	deleted int
	sawHunk bool
}

func loadAddedContent(ctx context.Context, workDir, baseRef, headRef, baselinePath, path string, rename bool, detection string) (addedContent, error) {
	args := []string{"diff", "--unified=0", "--no-color", "--no-ext-diff"}
	if rename {
		args = append(args, detection)
	} else {
		args = append(args, "--no-renames")
	}
	args = append(args, baseRef, headRef, "--", baselinePath)
	if path != baselinePath {
		args = append(args, path)
	}
	diff, err := git.Output(ctx, workDir, args...)
	if err != nil {
		return addedContent{}, fmt.Errorf("load added lines for %q: %w", path, err)
	}
	var added strings.Builder
	result := addedContent{}
	currentLine := 1
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			inHunk = false
			continue
		}
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			inHunk = true
			result.sawHunk = true
			start, _ := strconv.Atoi(match[1])
			for currentLine < start {
				added.WriteByte('\n')
				currentLine++
			}
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			added.WriteString(strings.TrimPrefix(line, "+"))
			added.WriteByte('\n')
			currentLine++
			result.added++
		case strings.HasPrefix(line, "-"):
			result.deleted++
			continue
		case strings.HasPrefix(line, " "):
			added.WriteByte('\n')
			currentLine++
		}
	}
	result.text = added.String()
	return result, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
