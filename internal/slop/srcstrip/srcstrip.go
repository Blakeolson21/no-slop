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
// # Why this is a scanner and not a set of regexes
//
// The first version paired region delimiters with regexes (`/\*.*?\*/` and
// "`[^`]*`") applied to the whole file. Positional pairing has no idea which
// delimiters are live, so a backtick that is NOT a raw-string delimiter pairs
// with one that is and everything between them is erased. Both carriers are
// ordinary Go: a backtick inside a `//` comment, and a backtick inside a
// double-quoted string, which is exactly the `"`"` idiom this repository uses
// to build regexes. Seven of this repository's own tracked .go files carry an
// odd number of backticks. The damage was not theoretical: real guard clauses
// vanished from the stripped view, so the removed-guard detector reported
// clauses the file still contained, and the test floor read a 29-test file as
// holding one. Both findings block with no exemption path.
//
// So regions are found by one left-to-right pass that knows what state it is
// in. A delimiter opens a region only when the scan is in code state; inside a
// string or a comment it is just a byte.
//
// # What is blanked and what is only tracked
//
// Comments and the multi-line literals that can hold arbitrary text are
// blanked, because those are the places a guard-shaped line can sit without
// being code. Single-line string literals are TRACKED but preserved: blanking
// them would erase the operand in `if role != "admin" {`, which is part of what
// tells that guard apart from every other one. Tracking them is still required,
// because their contents must not be able to open a region.
//
// Blanking preserves line numbering. A removed region becomes the same number
// of empty lines it occupied, so a caller can still report `file:line` against
// the original content.
//
// # Coverage
//
// The inert-text guarantee is per file kind and only as good as the table
// below. It covers Go, Python, the JavaScript family, Ruby, Java, Kotlin, Rust,
// and SQL. A file kind absent from the table is returned unchanged, so a check
// reading it is reading raw text again. Adding one means adding its comment and
// multi-line-literal spellings here rather than at the call site.
package srcstrip

import (
	"path/filepath"
	"strings"
)

// region is one span of non-code text, named by how it opens and closes.
type region struct {
	open  string
	close string
	// escape is the character that quotes the next byte inside this region. Zero
	// means the region has no escapes, which is what makes a raw string raw.
	escape byte
	// blank removes the region's text. A region that is not blanked is still
	// scanned, so a delimiter inside it cannot open anything.
	blank bool
	// lineAnchored requires the opener at column zero and the closer as the only
	// token on its line. Ruby's =begin/=end is the case.
	lineAnchored bool
	// endsAtNewline closes the region at the line's end rather than at a closer.
	endsAtNewline bool
}

// Spec describes how one file kind spells the regions that are not code.
//
// Order matters: regions are tried in sequence, so a longer opener has to come
// before a prefix of itself. `"""` before `"` is the case that matters, since
// checking `"` first would read a Java text block as an empty string followed
// by code.
type Spec struct {
	regions []region
	// rustRawStrings enables r"...", r#"..."#, r##"..."##, whose closer depends
	// on how many hashes the opener used.
	rustRawStrings bool
	// rubyHeredocs enables <<~IDENT, <<-IDENT, and <<IDENT bodies.
	rubyHeredocs bool
}

func lineComment(marker string) region {
	return region{open: marker, blank: true, endsAtNewline: true}
}

func blockComment(open, close string) region {
	return region{open: open, close: close, blank: true}
}

// blankedLiteral is a multi-line literal whose body is removed.
func blankedLiteral(open, close string, escape byte) region {
	return region{open: open, close: close, escape: escape, blank: true}
}

// trackedLiteral is a single-line literal whose body is preserved. It is
// scanned only so its contents cannot open a region.
func trackedLiteral(open, close string, escape byte) region {
	return region{open: open, close: close, escape: escape, endsAtNewline: true}
}

var (
	goSpec = Spec{regions: []region{
		lineComment("//"),
		blockComment("/*", "*/"),
		blankedLiteral("`", "`", 0),
		trackedLiteral(`"`, `"`, '\\'),
		trackedLiteral("'", "'", '\\'),
	}}
	pythonSpec = Spec{regions: []region{
		lineComment("#"),
		blankedLiteral(`"""`, `"""`, '\\'),
		blankedLiteral("'''", "'''", '\\'),
		trackedLiteral(`"`, `"`, '\\'),
		trackedLiteral("'", "'", '\\'),
	}}
	javascriptSpec = Spec{regions: []region{
		lineComment("//"),
		blockComment("/*", "*/"),
		blankedLiteral("`", "`", '\\'),
		trackedLiteral(`"`, `"`, '\\'),
		trackedLiteral("'", "'", '\\'),
	}}
	rubySpec = Spec{
		regions: []region{
			{open: "=begin", close: "=end", blank: true, lineAnchored: true},
			lineComment("#"),
			trackedLiteral(`"`, `"`, '\\'),
			trackedLiteral("'", "'", '\\'),
		},
		rubyHeredocs: true,
	}
	javaSpec = Spec{regions: []region{
		lineComment("//"),
		blockComment("/*", "*/"),
		blankedLiteral(`"""`, `"""`, '\\'),
		trackedLiteral(`"`, `"`, '\\'),
		trackedLiteral("'", "'", '\\'),
	}}
	kotlinSpec = Spec{regions: []region{
		lineComment("//"),
		blockComment("/*", "*/"),
		blankedLiteral(`"""`, `"""`, 0),
		trackedLiteral(`"`, `"`, '\\'),
		trackedLiteral("'", "'", '\\'),
	}}
	rustSpec = Spec{
		regions: []region{
			lineComment("//"),
			blockComment("/*", "*/"),
			trackedLiteral(`"`, `"`, '\\'),
			trackedLiteral("'", "'", '\\'),
		},
		rustRawStrings: true,
	}
	sqlSpec = Spec{regions: []region{
		lineComment("--"),
		blockComment("/*", "*/"),
		trackedLiteral("'", "'", 0),
		trackedLiteral(`"`, `"`, 0),
	}}
)

var specs = map[string]Spec{
	".go":   goSpec,
	".py":   pythonSpec,
	".rb":   rubySpec,
	".java": javaSpec,
	".kt":   kotlinSpec,
	".rs":   rustSpec,
	".sql":  sqlSpec,
	".js":   javascriptSpec,
	".jsx":  javascriptSpec,
	".ts":   javascriptSpec,
	".tsx":  javascriptSpec,
}

// For returns the spec for a path's extension.
func For(path string) (Spec, bool) {
	found, ok := specs[strings.ToLower(filepath.Ext(path))]
	return found, ok
}

// Blank replaces every non-code region with empty space, keeping the line count
// and every line's position intact.
func Blank(content string, spec Spec) string {
	s := &scanner{spec: spec, src: content}
	return s.run()
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

type scanner struct {
	spec Spec
	src  string
	out  strings.Builder
	pos  int
	// heredoc is the terminator a pending Ruby heredoc is waiting for. The body
	// starts after the newline that follows the opener, not at the opener.
	heredoc string
}

func (s *scanner) run() string {
	s.out.Grow(len(s.src))
	atLineStart := true
	for s.pos < len(s.src) {
		if s.src[s.pos] == '\n' {
			s.emit("\n")
			s.pos++
			if s.heredoc != "" {
				s.consumeHeredoc()
			}
			atLineStart = true
			continue
		}
		if s.openRegion(atLineStart) {
			atLineStart = false
			continue
		}
		if s.spec.rustRawStrings && s.openRustRawString() {
			atLineStart = false
			continue
		}
		if s.spec.rubyHeredocs {
			s.noteHeredoc()
		}
		if !isSpace(s.src[s.pos]) {
			atLineStart = false
		}
		s.emit(s.src[s.pos : s.pos+1])
		s.pos++
	}
	return s.out.String()
}

// openRegion consumes one region when the scan is sitting on its opener.
func (s *scanner) openRegion(atLineStart bool) bool {
	for _, candidate := range s.spec.regions {
		if !strings.HasPrefix(s.src[s.pos:], candidate.open) {
			continue
		}
		if candidate.lineAnchored && !atLineStart {
			continue
		}
		s.consumeRegion(candidate)
		return true
	}
	return false
}

// consumeRegion copies or blanks from the opener through the closer.
func (s *scanner) consumeRegion(r region) {
	start := s.pos
	s.pos += len(r.open)
	for s.pos < len(s.src) {
		current := s.src[s.pos]
		if r.endsAtNewline && current == '\n' {
			break
		}
		if r.escape != 0 && current == r.escape && s.pos+1 < len(s.src) {
			s.pos += 2
			continue
		}
		if r.close != "" && strings.HasPrefix(s.src[s.pos:], r.close) {
			if r.lineAnchored && !s.atColumnZero(s.pos) {
				s.pos++
				continue
			}
			s.pos += len(r.close)
			break
		}
		if r.close == "" && !r.endsAtNewline {
			break
		}
		s.pos++
	}
	s.finishRegion(r, start)
}

// finishRegion writes what the region contributes: nothing but its newlines when
// it is blanked, and its exact text when it is only tracked.
func (s *scanner) finishRegion(r region, start int) {
	text := s.src[start:s.pos]
	if !r.blank {
		s.emit(text)
		return
	}
	s.emit(strings.Repeat("\n", strings.Count(text, "\n")))
}

// openRustRawString handles r"...", r#"..."#, and any hash count above that. The
// closer is a quote followed by the same number of hashes the opener used, which
// is why it cannot be a fixed region.
func (s *scanner) openRustRawString() bool {
	cursor := s.pos
	if s.src[cursor] != 'r' {
		return false
	}
	if cursor > 0 && isIdentifierByte(s.src[cursor-1]) {
		return false
	}
	cursor++
	hashes := 0
	for cursor < len(s.src) && s.src[cursor] == '#' {
		hashes++
		cursor++
	}
	if cursor >= len(s.src) || s.src[cursor] != '"' {
		return false
	}
	cursor++
	closer := `"` + strings.Repeat("#", hashes)
	end := strings.Index(s.src[cursor:], closer)
	if end < 0 {
		return false
	}
	start := s.pos
	s.pos = cursor + end + len(closer)
	s.emit(strings.Repeat("\n", strings.Count(s.src[start:s.pos], "\n")))
	return true
}

// noteHeredoc records a pending Ruby heredoc terminator.
//
// The identifier is required to be upper case, which is the convention, and a
// terminator line must actually exist later in the file. Both conditions exist
// to keep `a << B` from being read as a heredoc: mistaking code for a region
// blanks live code, which is the exact failure this scanner replaced.
func (s *scanner) noteHeredoc() {
	if s.heredoc != "" || !strings.HasPrefix(s.src[s.pos:], "<<") {
		return
	}
	cursor := s.pos + 2
	if cursor < len(s.src) && (s.src[cursor] == '~' || s.src[cursor] == '-') {
		cursor++
	}
	quote := byte(0)
	if cursor < len(s.src) && (s.src[cursor] == '"' || s.src[cursor] == '\'') {
		quote = s.src[cursor]
		cursor++
	}
	start := cursor
	for cursor < len(s.src) && isIdentifierByte(s.src[cursor]) {
		cursor++
	}
	name := s.src[start:cursor]
	if name == "" || strings.ToUpper(name) != name || !startsUpperOrUnderscore(name) {
		return
	}
	if quote != 0 {
		if cursor >= len(s.src) || s.src[cursor] != quote {
			return
		}
	}
	if !s.hasTerminatorLine(cursor, name) {
		return
	}
	s.heredoc = name
}

// hasTerminatorLine reports whether a line holding only this identifier appears
// later. Without it an unterminated guess would blank the rest of the file.
func (s *scanner) hasTerminatorLine(from int, name string) bool {
	for _, line := range strings.Split(s.src[from:], "\n")[1:] {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// consumeHeredoc blanks the body that follows the opener's line.
func (s *scanner) consumeHeredoc() {
	name := s.heredoc
	s.heredoc = ""
	for s.pos < len(s.src) {
		end := strings.IndexByte(s.src[s.pos:], '\n')
		line := s.src[s.pos:]
		if end >= 0 {
			line = s.src[s.pos : s.pos+end]
		}
		s.emit("\n")
		if end < 0 {
			s.pos = len(s.src)
			return
		}
		s.pos += end + 1
		if strings.TrimSpace(line) == name {
			return
		}
	}
}

func (s *scanner) atColumnZero(index int) bool {
	return index == 0 || s.src[index-1] == '\n'
}

func (s *scanner) emit(text string) {
	s.out.WriteString(text)
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\f' || b == '\v'
}

func isIdentifierByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func startsUpperOrUnderscore(name string) bool {
	first := name[0]
	return first == '_' || (first >= 'A' && first <= 'Z')
}
