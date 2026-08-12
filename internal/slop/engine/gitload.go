package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

// LoadGitChanges materializes the committed change between two refs. Git
// invocations inherit the repository's bounded subprocess policy.
func LoadGitChanges(ctx context.Context, workDir, baseRef, headRef string) ([]Change, error) {
	if baseRef == "" || headRef == "" {
		return nil, fmt.Errorf("load git changes: base and head refs are required")
	}
	statusOutput, err := git.Run(ctx, workDir, "diff", "--name-status", "-z", "--find-renames=100%", baseRef, headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("load changed paths: %w", err)
	}
	entries := splitNUL(statusOutput)
	stats, err := loadNumStats(ctx, workDir, baseRef, headRef)
	if err != nil {
		return nil, err
	}

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
		if status == risk.Renamed {
			if index >= len(entries) {
				return nil, fmt.Errorf("load changed paths: rename is missing its destination")
			}
			path = entries[index]
			index++
		}
		change := Change{Path: path, Status: status}
		if stat, ok := stats[path]; ok && status != risk.Renamed {
			change.Added = stat.added
			change.Deleted = stat.deleted
		}
		if status != risk.Added {
			change.BaselineContent, err = showGitFile(ctx, workDir, baseRef, baselinePath)
			if err != nil {
				return nil, fmt.Errorf("load baseline %q: %w", path, err)
			}
		}
		if status != risk.Deleted {
			change.CurrentContent, err = showGitFile(ctx, workDir, headRef, path)
			if err != nil {
				return nil, fmt.Errorf("load current %q: %w", path, err)
			}
		}
		change.AddedContent, err = loadAddedContent(ctx, workDir, baseRef, headRef, path)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, nil
}

type numStat struct {
	added   int
	deleted int
}

func loadNumStats(ctx context.Context, workDir, baseRef, headRef string) (map[string]numStat, error) {
	output, err := git.Run(ctx, workDir, "diff", "--numstat", "-z", "--no-renames", baseRef, headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("load diff statistics: %w", err)
	}
	stats := make(map[string]numStat)
	for _, entry := range splitNUL(output) {
		fields := strings.SplitN(entry, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("load diff statistics: unexpected git numstat output")
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		stats[fields[2]] = numStat{added: added, deleted: deleted}
	}
	return stats, nil
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
	content, err := git.Run(ctx, workDir, "show", ref+":"+path)
	if err != nil {
		return "", err
	}
	if content != "" {
		content += "\n"
	}
	return content, nil
}

func loadAddedContent(ctx context.Context, workDir, baseRef, headRef, path string) (string, error) {
	diff, err := git.Run(ctx, workDir, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-renames", baseRef, headRef, "--", path)
	if err != nil {
		return "", fmt.Errorf("load added lines for %q: %w", path, err)
	}
	var added strings.Builder
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
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, " "):
			added.WriteByte('\n')
			currentLine++
		}
	}
	return added.String(), nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
