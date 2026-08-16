package risk

import (
	"path/filepath"
	"strings"
)

// language answers the two questions the rename rule needs to ask about a file:
// which other files could plausibly declare a symbol this one uses, and which
// byte ranges are code rather than commentary.
//
// The family is deliberately narrow. A Go file cannot supply the declaration
// evidence for a substitution inside a JavaScript module, and treating every
// declaration in a change set as one flat namespace is how a decoy written in
// one language paid for a weakening written in another.
type language struct {
	family  string
	comment commentSyntax
}

// commentSyntax names the byte sequences that begin commentary. Comment text is
// stripped before declarations are collected, because the token stream has no
// idea that `// TODO: let allowAnyone be dropped` is prose: `let` is a
// declaring keyword and `allowAnyone` reads as the name it introduces, so an
// untouched TODO in an unrelated file was enough to make a cross-package guard
// substitution look like a rename.
type commentSyntax struct {
	line   []string
	blocks [][2]string
}

var (
	cComments       = commentSyntax{line: []string{"//"}, blocks: [][2]string{{"/*", "*/"}}}
	hashComments    = commentSyntax{line: []string{"#"}}
	rubyComments    = commentSyntax{line: []string{"#"}, blocks: [][2]string{{"=begin", "=end"}}}
	sqlComments     = commentSyntax{line: []string{"--"}, blocks: [][2]string{{"/*", "*/"}}}
	luaComments     = commentSyntax{line: []string{"--"}, blocks: [][2]string{{"--[[", "]]"}}}
	lispComments    = commentSyntax{line: []string{";"}}
	erlangComments  = commentSyntax{line: []string{"%"}}
	haskellComments = commentSyntax{line: []string{"--"}, blocks: [][2]string{{"{-", "-}"}}}

	// unknownComments is the fail-closed union for an extension nobody listed.
	// Stripping a byte range that was really code loses a declaration, which
	// costs a review round; failing to strip a comment invents one, which is
	// the hole this exists to close. So the unknown case over-strips.
	unknownComments = commentSyntax{
		line:   []string{"//", "#", "--", ";", "%"},
		blocks: [][2]string{{"/*", "*/"}, {"<!--", "-->"}, {"{-", "-}"}},
	}
)

// languagesByExtension groups the extensions whose declarations can legitimately
// satisfy one another. TypeScript and JavaScript share a family because their
// import graphs genuinely cross; nothing else here does.
var languagesByExtension = map[string]language{
	".go": {family: "go", comment: cComments},

	".js": {family: "ecmascript", comment: cComments}, ".jsx": {family: "ecmascript", comment: cComments},
	".mjs": {family: "ecmascript", comment: cComments}, ".cjs": {family: "ecmascript", comment: cComments},
	".ts": {family: "ecmascript", comment: cComments}, ".tsx": {family: "ecmascript", comment: cComments},
	".mts": {family: "ecmascript", comment: cComments}, ".cts": {family: "ecmascript", comment: cComments},

	".py": {family: "python", comment: hashComments}, ".pyi": {family: "python", comment: hashComments},

	".rb": {family: "ruby", comment: rubyComments}, ".rake": {family: "ruby", comment: rubyComments},
	".gemspec": {family: "ruby", comment: rubyComments},

	".c": {family: "c", comment: cComments}, ".h": {family: "c", comment: cComments},
	".cc": {family: "c", comment: cComments}, ".cpp": {family: "c", comment: cComments},
	".cxx": {family: "c", comment: cComments}, ".hpp": {family: "c", comment: cComments},
	".hh": {family: "c", comment: cComments}, ".hxx": {family: "c", comment: cComments},
	".m": {family: "c", comment: cComments}, ".mm": {family: "c", comment: cComments},

	".java": {family: "java", comment: cComments},
	".kt":   {family: "kotlin", comment: cComments}, ".kts": {family: "kotlin", comment: cComments},
	".swift": {family: "swift", comment: cComments},
	".rs":    {family: "rust", comment: cComments},
	".php":   {family: "php", comment: commentSyntax{line: []string{"//", "#"}, blocks: [][2]string{{"/*", "*/"}}}},
	".cs":    {family: "csharp", comment: cComments},
	".scala": {family: "scala", comment: cComments}, ".sc": {family: "scala", comment: cComments},
	".dart":   {family: "dart", comment: cComments},
	".zig":    {family: "zig", comment: cComments},
	".groovy": {family: "groovy", comment: cComments},

	".sh": {family: "shell", comment: hashComments}, ".bash": {family: "shell", comment: hashComments},
	".zsh": {family: "shell", comment: hashComments}, ".ksh": {family: "shell", comment: hashComments},

	".pl": {family: "perl", comment: hashComments}, ".pm": {family: "perl", comment: hashComments},
	".ex": {family: "elixir", comment: hashComments}, ".exs": {family: "elixir", comment: hashComments},
	".r": {family: "r", comment: hashComments}, ".jl": {family: "julia", comment: hashComments},
	".nim": {family: "nim", comment: hashComments}, ".cr": {family: "crystal", comment: hashComments},

	".sql": {family: "sql", comment: sqlComments},
	".lua": {family: "lua", comment: luaComments},
	".hs":  {family: "haskell", comment: haskellComments},
	".erl": {family: "erlang", comment: erlangComments}, ".hrl": {family: "erlang", comment: erlangComments},

	".clj": {family: "clojure", comment: lispComments}, ".cljs": {family: "clojure", comment: lispComments},
	".cljc": {family: "clojure", comment: lispComments}, ".edn": {family: "clojure", comment: lispComments},
	".el": {family: "lisp", comment: lispComments}, ".lisp": {family: "lisp", comment: lispComments},
	".scm": {family: "lisp", comment: lispComments}, ".rkt": {family: "lisp", comment: lispComments},
	".vim": {family: "vim", comment: commentSyntax{line: []string{`"`}}},
}

// sourceLanguage resolves a path to its declaration family and comment syntax.
// An unrecognised extension gets a family of its own, so a file whose language
// this table has never heard of can still be a rename campaign with its own
// siblings but can never vouch for a substitution in a language it is not.
func sourceLanguage(path string) language {
	extension := strings.ToLower(filepath.Ext(filepath.ToSlash(path)))
	if known, ok := languagesByExtension[extension]; ok {
		return known
	}
	if extension == "" {
		// An extensionless executable script is the shape an allowlist misses.
		// It gets the union comment set and a family keyed on its base name, so
		// it neither vouches for anything else nor loses its own declarations.
		return language{family: "path:" + strings.ToLower(filepath.Base(filepath.ToSlash(path))), comment: unknownComments}
	}
	return language{family: "ext:" + extension, comment: unknownComments}
}

// commentEnd returns the index just past the comment starting at index, or
// index itself when no comment starts there. It is called only from the
// tokenizer's top level, so a marker inside a string literal is never reached:
// the quote branch has already consumed the whole literal by then.
func commentEnd(content string, index int, comments commentSyntax) int {
	rest := content[index:]
	for _, block := range comments.blocks {
		if !strings.HasPrefix(rest, block[0]) {
			continue
		}
		after := index + len(block[0])
		if closing := strings.Index(content[after:], block[1]); closing >= 0 {
			return after + closing + len(block[1])
		}
		return len(content)
	}
	for _, marker := range comments.line {
		if !strings.HasPrefix(rest, marker) {
			continue
		}
		if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
			return index + newline
		}
		return len(content)
	}
	return index
}
