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

// language describes how one file kind spells tests, comments, and giving up.
type language struct {
	declaration    *regexp.Regexp
	lineComment    []string
	blockComment   *regexp.Regexp
	perLine        bool
	annotationLead bool
}

// languages recognises a test declaration and, just as importantly, knows what
// a comment looks like in that file kind. A single comment rule that treated
// `#` as a comment everywhere blanked every Rust `#[test]`, which is the shape
// of mistake that makes a floor quietly stop counting.
var languages = map[string]language{
	".go": {
		declaration: regexp.MustCompile(`^\s*func\s+Test[A-Za-z0-9_]+\s*\(`),
		lineComment: []string{"//"},
		perLine:     true,
	},
	".py": {
		declaration:    regexp.MustCompile(`^\s*(?:async\s+)?def\s+test_[A-Za-z0-9_]+\s*\(`),
		lineComment:    []string{"#"},
		blockComment:   regexp.MustCompile(`(?s)"""(?:.*?)"""|'''(?:.*?)'''`),
		perLine:        true,
		annotationLead: true,
	},
	".rb": {
		declaration: regexp.MustCompile(`^\s*(?:it|specify|test)\s+['"]`),
		lineComment: []string{"#"},
		perLine:     true,
	},
	".java": {
		declaration:    regexp.MustCompile(`^\s*@Test\b`),
		lineComment:    []string{"//"},
		blockComment:   regexp.MustCompile(`(?s)/\*.*?\*/`),
		perLine:        true,
		annotationLead: true,
	},
	".kt": {
		declaration:    regexp.MustCompile(`^\s*@Test\b`),
		lineComment:    []string{"//"},
		blockComment:   regexp.MustCompile(`(?s)/\*.*?\*/`),
		perLine:        true,
		annotationLead: true,
	},
	".rs": {
		declaration:    regexp.MustCompile(`^\s*#\[test\]`),
		lineComment:    []string{"//"},
		blockComment:   regexp.MustCompile(`(?s)/\*.*?\*/`),
		perLine:        true,
		annotationLead: true,
	},
}

// javascriptLanguage is shared by the four JavaScript-family extensions. Its
// declaration is deliberately not line-anchored, because a whole `describe`
// block routinely sits on one line. The skip spellings (`it.skip(`, `xit(`)
// cannot match this pattern, so they are excluded by construction rather than
// by a second scan.
var javascriptLanguage = language{
	declaration:  regexp.MustCompile(`\b(?:it|test)\s*\(`),
	lineComment:  []string{"//"},
	blockComment: regexp.MustCompile(`(?s)/\*.*?\*/`),
}

func languageFor(ext string) (language, bool) {
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx":
		return javascriptLanguage, true
	}
	found, ok := languages[ext]
	return found, ok
}

// skipMarkers are the ways a test stays declared while asserting nothing. A
// count-only floor was satisfied by replacing every body with a skip, so a
// skipped test is not counted: the inventory measures how much of the suite
// still runs, not how many function names survive.
var skipMarkers = []*regexp.Regexp{
	regexp.MustCompile(`\bt\.Skip(?:Now|f)?\s*\(`),
	regexp.MustCompile(`\btesting\.Short\s*\(\s*\)`),
	regexp.MustCompile(`@(?:pytest\.mark\.)?skip\b|@Disabled\b|@Ignore\b`),
	regexp.MustCompile(`\bpytest\.skip\s*\(|\bself\.skipTest\s*\(`),
	regexp.MustCompile(`#\[ignore\]`),
}

var annotationLine = regexp.MustCompile(`^\s*(?:@|#\[)`)

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

// Count returns the number of recognizable test declarations that still run.
func Count(files []File) int {
	total := 0
	for _, file := range files {
		spec, ok := languageFor(strings.ToLower(filepath.Ext(file.Path)))
		if !ok {
			continue
		}
		lines := strings.Split(stripComments(file.Content, spec), "\n")
		if !spec.perLine {
			for _, line := range lines {
				total += len(spec.declaration.FindAllStringIndex(line, -1))
			}
			continue
		}
		for index, line := range lines {
			if !spec.declaration.MatchString(line) {
				continue
			}
			if declarationIsSkipped(lines, index, spec) {
				continue
			}
			total++
		}
	}
	return total
}

// declarationIsSkipped reports whether the declaration at index gives up before
// asserting anything: a skip in its body before the next declaration, or, for
// annotation-led languages, a disabling attribute directly above it.
func declarationIsSkipped(lines []string, index int, spec language) bool {
	if spec.annotationLead {
		for cursor := index - 1; cursor >= 0 && annotationLine.MatchString(lines[cursor]); cursor-- {
			if matchesSkipMarker(lines[cursor]) {
				return true
			}
		}
	}
	for cursor := index; cursor < len(lines); cursor++ {
		if cursor > index && spec.declaration.MatchString(lines[cursor]) {
			return false
		}
		if matchesSkipMarker(lines[cursor]) {
			return true
		}
	}
	return false
}

func matchesSkipMarker(line string) bool {
	for _, marker := range skipMarkers {
		if marker.MatchString(line) {
			return true
		}
	}
	return false
}

// stripComments blanks out commented regions before counting. Counting them
// meant a commented-out test still held its place in the inventory, so the
// floor could be satisfied by deleting nothing and running nothing.
func stripComments(content string, spec language) string {
	blank := func(match string) string {
		return strings.Repeat("\n", strings.Count(match, "\n"))
	}
	if spec.blockComment != nil {
		content = spec.blockComment.ReplaceAllStringFunc(content, blank)
	}
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, marker := range spec.lineComment {
			if strings.HasPrefix(trimmed, marker) {
				lines[index] = ""
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
