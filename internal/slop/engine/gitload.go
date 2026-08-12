package engine

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
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
		if status != risk.Added {
			change.BaselineContent, err = showGitFile(ctx, workDir, baseRef, baselinePath)
			if err != nil {
				return nil, fmt.Errorf("load baseline %q: %w", path, err)
			}
		}
		if status == risk.Modified {
			change.BaselineContext, err = loadBaselineSiblingContent(ctx, workDir, baseRef, baselinePath)
			if err != nil {
				return nil, fmt.Errorf("load baseline sibling context for %q: %w", path, err)
			}
		}
		if status != risk.Deleted {
			change.CurrentContent, err = showGitFile(ctx, workDir, headRef, path)
			if err != nil {
				return nil, fmt.Errorf("load current %q: %w", path, err)
			}
		}
		change.AddedContent, change.Added, change.Deleted, err = loadAddedContent(ctx, workDir, baseRef, headRef, baselinePath, path, isRename, renameDetection)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	attachDeletedCommentBaselines(changes)
	return changes, nil
}

func attachDeletedCommentBaselines(changes []Change) {
	type candidate struct {
		deleted int
		added   int
		score   int
	}
	identities := make(map[int]goFileIdentity)
	for index := range changes {
		if (changes[index].Status == risk.Deleted || changes[index].Status == risk.Added) && filepath.Ext(changes[index].Path) == ".go" {
			content := changes[index].CurrentContent
			if changes[index].Status == risk.Deleted {
				content = changes[index].BaselineContent
			}
			identities[index] = parseGoFileIdentity(content)
		}
	}
	var candidates []candidate
	for deleted := range changes {
		baseline, ok := identities[deleted]
		if !ok || changes[deleted].Status != risk.Deleted {
			continue
		}
		for added := range changes {
			current, ok := identities[added]
			if !ok || changes[added].Status != risk.Added {
				continue
			}
			score := goLineageScore(baseline, current)
			if score > 0 {
				candidates = append(candidates, candidate{deleted: deleted, added: added, score: score})
			}
		}
	}
	bestDeleted := make(map[int]candidate)
	ambiguousDeleted := make(map[int]bool)
	bestAdded := make(map[int]candidate)
	ambiguousAdded := make(map[int]bool)
	for _, match := range candidates {
		if best, ok := bestDeleted[match.deleted]; !ok || match.score > best.score {
			bestDeleted[match.deleted] = match
			ambiguousDeleted[match.deleted] = false
		} else if match.score == best.score {
			ambiguousDeleted[match.deleted] = true
		}
		if best, ok := bestAdded[match.added]; !ok || match.score > best.score {
			bestAdded[match.added] = match
			ambiguousAdded[match.added] = false
		} else if match.score == best.score {
			ambiguousAdded[match.added] = true
		}
	}
	for added, match := range bestAdded {
		if ambiguousAdded[added] || ambiguousDeleted[match.deleted] || bestDeleted[match.deleted].added != added {
			continue
		}
		changes[added].CommentBaselinePath = changes[match.deleted].Path
		changes[added].CommentBaselineContent = changes[match.deleted].BaselineContent
		changes[added].CommentAddedContent = changes[added].AddedContent
	}
}

type goFileIdentity struct {
	packageName  string
	declarations map[string]int
	comments     map[string]bool
}

func goLineageScore(baseline, current goFileIdentity) int {
	if baseline.packageName == "" || baseline.packageName != current.packageName || !sharesStringKey(baseline.comments, current.comments) {
		return 0
	}
	score := 0
	for declaration, size := range baseline.declarations {
		if current.declarations[declaration] > 0 {
			score += size
		}
	}
	if score < 32 {
		return 0
	}
	return score
}

func parseGoFileIdentity(content string) goFileIdentity {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "", content, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return goFileIdentity{}
	}
	identity := goFileIdentity{
		packageName:  parsed.Name.Name,
		declarations: make(map[string]int),
		comments:     make(map[string]bool),
	}
	source := []byte(content)
	for _, declaration := range parsed.Decls {
		start := files.PositionFor(declaration.Pos(), false).Offset
		end := files.PositionFor(declaration.End(), false).Offset
		if start >= 0 && end > start && end <= len(source) {
			text := string(source[start:end])
			identity.declarations[text] = len(text)
		}
	}
	for _, group := range parsed.Comments {
		if text := strings.TrimSpace(group.Text()); text != "" {
			identity.comments[text] = true
		}
	}
	return identity
}

func sharesStringKey(left, right map[string]bool) bool {
	for value := range left {
		if right[value] {
			return true
		}
	}
	return false
}

func loadBaselineSiblingContent(ctx context.Context, workDir, baseRef, path string) (string, error) {
	dir := filepath.ToSlash(filepath.Dir(path))
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "", nil
	}
	scope := dir
	if scope == "." {
		scope = ":(top)"
	}
	output, err := git.Run(ctx, workDir, "ls-tree", "-r", "--name-only", "-z", baseRef, "--", scope)
	if err != nil {
		return "", err
	}
	var context strings.Builder
	for _, sibling := range splitNUL(output) {
		if sibling == path || filepath.ToSlash(filepath.Dir(sibling)) != dir || strings.ToLower(filepath.Ext(sibling)) != ext {
			continue
		}
		content, err := showGitFile(ctx, workDir, baseRef, sibling)
		if err != nil {
			return "", err
		}
		context.WriteString(content)
		context.WriteByte('\n')
	}
	return context.String(), nil
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

func loadAddedContent(ctx context.Context, workDir, baseRef, headRef, baselinePath, path string, rename bool, detection string) (string, int, int, error) {
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
		return "", 0, 0, fmt.Errorf("load added lines for %q: %w", path, err)
	}
	var added strings.Builder
	addedCount := 0
	deletedCount := 0
	currentLine := 1
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			inHunk = false
			continue
		}
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			inHunk = true
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
			addedCount++
		case strings.HasPrefix(line, "-"):
			deletedCount++
			continue
		case strings.HasPrefix(line, " "):
			added.WriteByte('\n')
			currentLine++
		}
	}
	return added.String(), addedCount, deletedCount, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
