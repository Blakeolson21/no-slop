// Package testfloor compares discoverable test counts across two revisions.
package testfloor

import (
	"path/filepath"
	"regexp"
	"strings"
)

// File is one source artifact from a revision.
type File struct {
	Path    string
	Content string
}

// Result reports whether the current test inventory stays at or above its
// baseline count.
type Result struct {
	Baseline int
	Current  int
	Delta    int
	Passed   bool
}

var testPatterns = map[string]*regexp.Regexp{
	".go":   regexp.MustCompile(`(?m)^\s*func\s+Test[A-Za-z0-9_]+\s*\(`),
	".py":   regexp.MustCompile(`(?m)^\s*def\s+test_[A-Za-z0-9_]+\s*\(`),
	".rb":   regexp.MustCompile(`(?m)^\s*(?:it|specify|test)\s+['"]`),
	".java": regexp.MustCompile(`(?m)^\s*@Test\b`),
	".kt":   regexp.MustCompile(`(?m)^\s*@Test\b`),
	".rs":   regexp.MustCompile(`(?m)^\s*#\[test\]`),
	".js":   regexp.MustCompile(`\b(?:it|test)\s*\(`),
	".jsx":  regexp.MustCompile(`\b(?:it|test)\s*\(`),
	".ts":   regexp.MustCompile(`\b(?:it|test)\s*\(`),
	".tsx":  regexp.MustCompile(`\b(?:it|test)\s*\(`),
}

// Compare counts recognizable tests in each revision and applies the floor.
func Compare(baseline, current []File) Result {
	result := Result{
		Baseline: Count(baseline),
		Current:  Count(current),
	}
	result.Delta = result.Current - result.Baseline
	result.Passed = result.Delta >= 0
	return result
}

// Count returns the number of recognizable test declarations.
func Count(files []File) int {
	total := 0
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file.Path))
		pattern := testPatterns[ext]
		if pattern == nil {
			continue
		}
		total += len(pattern.FindAllStringIndex(file.Content, -1))
	}
	return total
}
