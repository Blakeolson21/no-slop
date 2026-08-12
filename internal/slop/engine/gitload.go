package engine

import (
	"context"
	"fmt"
	"go/ast"
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
	var candidates []candidate
	for deleted := range changes {
		if changes[deleted].Status != risk.Deleted || filepath.Ext(changes[deleted].Path) != ".go" {
			continue
		}
		for added := range changes {
			if changes[added].Status != risk.Added || filepath.Ext(changes[added].Path) != ".go" {
				continue
			}
			score := goLineageScore(changes[deleted].BaselineContent, changes[added].CurrentContent)
			if score >= 2 {
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

func goLineageScore(baseline, current string) int {
	baselinePackage, baselineNames := goFileIdentity(baseline)
	currentPackage, currentNames := goFileIdentity(current)
	if baselinePackage == "" || baselinePackage != currentPackage {
		return 0
	}
	score := 0
	for name := range baselineNames {
		if currentNames[name] {
			score++
		}
	}
	return score
}

func goFileIdentity(content string) (string, map[string]bool) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "", content, parser.SkipObjectResolution)
	if err != nil {
		return "", nil
	}
	names := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			if value.Name.Name != "init" {
				names[value.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					names[item.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range item.Names {
						if name.Name != "_" {
							names[name.Name] = true
						}
					}
				}
			}
		}
	}
	return parsed.Name.Name, names
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
	for _, line := range strings.Split(diff, "\n") {
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			start, _ := strconv.Atoi(match[1])
			for currentLine < start {
				added.WriteByte('\n')
				currentLine++
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added.WriteString(strings.TrimPrefix(line, "+"))
			added.WriteByte('\n')
			currentLine++
			addedCount++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
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
