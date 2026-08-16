package srcstrip_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/srcstrip"
)

// guardLine is the shape both callers care about: a refusing check that must
// survive stripping when it is code, and must not survive when it is inert text.
const guardLine = `if role != "admin" {`

// TestALiveDelimiterIsNotPairedWithOneInsideACommentOrString is the round-6
// regression. Region delimiters were paired positionally by regex, so a backtick
// that is not a raw-string delimiter paired with one that is and the code
// between them was erased. Both carriers below are ordinary Go, and seven of
// this repository's own tracked .go files carry an odd number of backticks.
func TestALiveDelimiterIsNotPairedWithOneInsideACommentOrString(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		content string
	}{
		{
			// The comment's backtick is not a delimiter, but positional pairing
			// matched it against the opener of the real raw string below and
			// blanked everything in between, which is the guard.
			name: "backtick inside a line comment",
			content: "package p\n\n" +
				"// the ` in this sentence is not a raw string\n" +
				"func Authorize(role string) error {\n\t" + guardLine + "\n\t\treturn errForbidden\n\t}\n\treturn nil\n}\n\n" +
				"var pattern = `^[a-z]+$`\n",
		},
		{
			// This repository's own idiom for building a regex that contains a
			// backtick. Same mispairing, no comment involved.
			name: "backtick inside a double-quoted string",
			content: "package p\n\n" +
				"var quoted = regexp.MustCompile(\"a`b\")\n\n" +
				"func Authorize(role string) error {\n\t" + guardLine + "\n\t\treturn errForbidden\n\t}\n\treturn nil\n}\n\n" +
				"var raw = `^[a-z]+$`\n",
		},
		{
			name: "odd backtick parity across the file",
			content: "package p\n\n" +
				"var one = `first raw`\n" +
				"// a trailing ` makes the parity odd\n" +
				"func Authorize(role string) error {\n\t" + guardLine + "\n\t\treturn errForbidden\n\t}\n\treturn nil\n}\n\n" +
				"var two = `second raw`\n",
		},
	} {
		stripped := srcstrip.BlankPath("policy.go", probe.content)
		if !strings.Contains(stripped, guardLine) {
			t.Errorf("%s: stripping erased live code:\n%s", probe.name, stripped)
		}
		if lines(stripped) != lines(probe.content) {
			t.Errorf("%s: line count moved from %d to %d", probe.name, lines(probe.content), lines(stripped))
		}
	}
}

// TestInertTextIsBlankedForEveryCoveredLanguage pins the coverage the package
// doc claims. Each probe parks the same guard inside that language's multi-line
// non-code region, which is the shape that let a deleted authorization guard
// keep matching at head.
func TestInertTextIsBlankedForEveryCoveredLanguage(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		path    string
		content string
	}{
		{"go raw string", "policy.go", "package p\n\nconst legacy = `\n" + guardLine + "\n`\n"},
		{"go block comment", "policy.go", "package p\n\n/*\n" + guardLine + "\n*/\n"},
		{"python triple quote", "policy.py", "legacy = \"\"\"\n" + guardLine + "\n\"\"\"\n"},
		{"javascript template", "policy.js", "const legacy = `\n" + guardLine + "\n`;\n"},
		{"ruby block comment", "policy.rb", "=begin\n" + guardLine + "\n=end\n"},
		{"ruby heredoc", "policy.rb", "legacy = <<~LEGACY\n" + guardLine + "\nLEGACY\n"},
		{"java text block", "Policy.java", "class Policy {\n  String legacy = \"\"\"\n" + guardLine + "\n\"\"\";\n}\n"},
		{"kotlin raw string", "Policy.kt", "val legacy = \"\"\"\n" + guardLine + "\n\"\"\"\n"},
		{"rust raw string", "policy.rs", "const LEGACY: &str = r#\"\n" + guardLine + "\n\"#;\n"},
		{"sql block comment", "policy.sql", "/*\n" + guardLine + "\n*/\n"},
	} {
		stripped := srcstrip.BlankPath(probe.path, probe.content)
		if strings.Contains(stripped, guardLine) {
			t.Errorf("%s: inert text survived stripping:\n%s", probe.name, stripped)
		}
		if lines(stripped) != lines(probe.content) {
			t.Errorf("%s: line count moved from %d to %d", probe.name, lines(probe.content), lines(stripped))
		}
	}
}

// TestLiveCodeSurvivesForEveryCoveredLanguage is the false-positive control. A
// stripper that blanks real code turns the removed-guard detector into a source
// of unappealable blocking findings, which is worse than the bypass it closes.
func TestLiveCodeSurvivesForEveryCoveredLanguage(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		path    string
		content string
	}{
		{"go", "policy.go", "package p\n\nfunc a() {\n\t" + guardLine + "\n\t\treturn errForbidden\n\t}\n}\n"},
		{"python", "policy.py", "def a():\n    " + guardLine + "\n        return False\n"},
		{"javascript", "policy.js", "function a() {\n  " + guardLine + "\n    return false;\n  }\n}\n"},
		{"ruby", "policy.rb", "def a\n  " + guardLine + "\n    return false\n  end\nend\n"},
		{"java", "Policy.java", "class Policy {\n  void a() {\n    " + guardLine + "\n    }\n  }\n}\n"},
		{"kotlin", "Policy.kt", "fun a() {\n  " + guardLine + "\n  }\n}\n"},
		{"rust", "policy.rs", "fn a() {\n    " + guardLine + "\n    }\n}\n"},
		{"sql", "policy.sql", "-- header\nselect 1; " + guardLine + "\n"},
	} {
		stripped := srcstrip.BlankPath(probe.path, probe.content)
		if !strings.Contains(stripped, guardLine) {
			t.Errorf("%s: stripping erased live code:\n%s", probe.name, stripped)
		}
	}
}

// TestShiftOperatorIsNotReadAsAHeredoc guards the Ruby heredoc rule against the
// direction that costs the most. Mistaking code for a region blanks live code,
// so the identifier must be upper case AND a terminator line must exist.
func TestShiftOperatorIsNotReadAsAHeredoc(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		content string
	}{
		{"lower case operand", "list << item\n" + guardLine + "\n  return false\nend\n"},
		{"upper case operand with no terminator line", "list << ITEM\n" + guardLine + "\n  return false\nend\n"},
	} {
		stripped := srcstrip.BlankPath("policy.rb", probe.content)
		if !strings.Contains(stripped, guardLine) {
			t.Errorf("%s: a shift expression was read as a heredoc and blanked live code:\n%s", probe.name, stripped)
		}
	}
}

// TestUnknownExtensionIsReturnedUnchanged names the boundary of the coverage
// claim rather than leaving a caller to assume every file kind is stripped.
func TestUnknownExtensionIsReturnedUnchanged(t *testing.T) {
	t.Parallel()

	content := "/*\n" + guardLine + "\n*/\n"
	if got := srcstrip.BlankPath("policy.erl", content); got != content {
		t.Fatalf("an unrecognised extension was modified:\n%s", got)
	}
}

func lines(value string) int {
	return strings.Count(value, "\n")
}
