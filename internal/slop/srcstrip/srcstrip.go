// Package srcstrip blanks the regions of a source file that are not code.
//
// Two mechanical checks need the same thing and had drifted apart. The
// test-count floor learned it first: counting a commented-out test let a change
// satisfy the floor by deleting nothing and running nothing. The removed-guard
// detector then hit the mirror image, and the cheaper direction, because it
// compares both revisions: keeping a deleted authorization guard as INERT TEXT
// at head, inside a block comment or a raw string literal, made the clause
// still appear at head, so the removal netted to zero and no finding was
// produced at all. That costs the author no compilable code, which made it
// cheaper than the padding shapes the same detector already refuses.
//
// One implementation serves both, because it is one defect family: a check that
// reads raw lines is reading text the compiler never sees.
//
// Blanking preserves line numbering. A removed region becomes the same number
// of empty lines it occupied, so a caller can still report `file:line` against
// the original content.
package srcstrip

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Spec describes how one file kind spells the regions that are not code.
//
// BlockRegions covers block comments AND the multi-line string literals that
// can hold arbitrary text, because for this purpose they are the same thing:
// somewhere a guard-shaped line can sit without being code. Single-line string
// literals are deliberately NOT stripped. Blanking them would erase the
// operand in `if role != "admin" {`, which is part of what tells that guard
// apart from every other one, and a lone line cannot open a region anyway.
type Spec struct {
	LineComment  []string
	BlockRegions []*regexp.Regexp
}

var (
	cStyleBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	backtickRawString  = regexp.MustCompile("(?s)`[^`]*`")
	pythonTripleQuote  = regexp.MustCompile(`(?s)"""(?:.*?)"""|'''(?:.*?)'''`)
)

var specs = map[string]Spec{
	".go":   {LineComment: []string{"//"}, BlockRegions: []*regexp.Regexp{cStyleBlockComment, backtickRawString}},
	".py":   {LineComment: []string{"#"}, BlockRegions: []*regexp.Regexp{pythonTripleQuote}},
	".rb":   {LineComment: []string{"#"}},
	".java": {LineComment: []string{"//"}, BlockRegions: []*regexp.Regexp{cStyleBlockComment}},
	".kt":   {LineComment: []string{"//"}, BlockRegions: []*regexp.Regexp{cStyleBlockComment}},
	".rs":   {LineComment: []string{"//"}, BlockRegions: []*regexp.Regexp{cStyleBlockComment}},
	".sql":  {LineComment: []string{"--"}, BlockRegions: []*regexp.Regexp{cStyleBlockComment}},
}

// javascriptSpec is shared by the four JavaScript-family extensions. The
// backtick region is a template literal, which spans lines like Go's raw string.
var javascriptSpec = Spec{
	LineComment:  []string{"//"},
	BlockRegions: []*regexp.Regexp{cStyleBlockComment, backtickRawString},
}

// For returns the spec for a path's extension.
func For(path string) (Spec, bool) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".js", ".jsx", ".ts", ".tsx":
		return javascriptSpec, true
	default:
		found, ok := specs[ext]
		return found, ok
	}
}

// Blank replaces every non-code region with empty space, keeping the line count
// and every line's position intact.
func Blank(content string, spec Spec) string {
	keepLineCount := func(match string) string {
		return strings.Repeat("\n", strings.Count(match, "\n"))
	}
	for _, region := range spec.BlockRegions {
		content = region.ReplaceAllStringFunc(content, keepLineCount)
	}
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, marker := range spec.LineComment {
			if strings.HasPrefix(trimmed, marker) {
				lines[index] = ""
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// BlankPath is the convenience form for callers that hold a path. Content for
// an unrecognised extension is returned unchanged.
func BlankPath(path, content string) string {
	spec, ok := For(path)
	if !ok {
		return content
	}
	return Blank(content, spec)
}
