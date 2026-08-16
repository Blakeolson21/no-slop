package leakscan_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/leakscan"
)

// The binary decision moved to the scanner's own bytes in round 4 and closed
// both NUL shapes. It stayed keyed on the C0 range, and round 5 walked around
// it with one character: IsBinaryContent calls every byte above 0x1f text, so
// an invisible character that is not a C0 control leaves the file "text" and
// breaks the credential regex at the same time. One U+200B inside an AWS key
// returned "leak scan completed (0 findings)" and passed at exit 0, which is
// the round-4 T3 sentence unchanged.
//
// The answer here is not a fifth widening of a byte range. Any character class
// this code names can be walked around by the next character outside it, in the
// same way each byte range was. What changes is that disagreement between two
// readings of the same bytes is itself the trigger: when removing the invisible
// characters produces different text, BOTH readings are scanned, so hiding a
// credential from the scanner now requires hiding it from the regex in both
// renderings at once.

// awsKey is the AWS documentation's own example key, not a live credential.
const awsKey = "AKIAIOSFODNN7EXAMPLE"

func TestInvisibleCharactersInsideACredentialDoNotBlindTheScan(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name   string
		hidden string
	}{
		{"U+200B zero width space", "\u200b"},
		{"U+0085 next line", "\u0085"},
		{"U+2028 line separator", "\u2028"},
		{"U+2029 paragraph separator", "\u2029"},
		{"U+FEFF zero width no-break space", "\ufeff"},
		{"U+00AD soft hyphen", "\u00ad"},
		{"U+200E left-to-right mark", "\u200e"},
		{"U+2060 word joiner", "\u2060"},
		{"U+061C arabic letter mark", "\u061c"},
		{"U+180E mongolian vowel separator", "\u180e"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			// The character goes INSIDE the key, which is where it defeats the
			// regex while staying invisible in every editor and diff view a
			// reviewer would open it in.
			hidden := awsKey[:4] + probe.hidden + awsKey[4:]
			result := leakscan.Scan([]leakscan.File{{
				Path:    "docs/notes.txt",
				Content: "release notes\naws key = " + hidden + "\n",
			}}, leakscan.Options{})
			if len(result.Findings) == 0 {
				t.Fatalf("an invisible character hid a credential from the mandatory scan: %q", hidden)
			}
			for _, finding := range result.Findings {
				if strings.Contains(finding.Description, awsKey) {
					t.Fatalf("finding description echoed the credential: %+v", finding)
				}
			}
		})
	}
}

// TestInvisibleCharactersAreAlsoReadThroughTheBinaryRenderings closes the same
// character class on the other route. A blob the scanner calls binary was
// already read two ways; neither of those removed a zero-width space, so the
// two fixes have to compose rather than be chosen between.
func TestInvisibleCharactersAreAlsoReadThroughTheBinaryRenderings(t *testing.T) {
	t.Parallel()

	hidden := awsKey[:4] + "\u200b" + awsKey[4:]
	result := leakscan.Scan([]leakscan.File{{
		Path:    "docs/notes.bin",
		Content: "\x00release notes\naws key = " + hidden + "\n",
		Binary:  true,
	}}, leakscan.Options{})
	if len(result.Findings) == 0 {
		t.Fatal("a NUL plus a zero-width space hid a credential from both renderings")
	}
}

// TestHasInvisibleRunesLeavesOrdinaryTextAlone is the cost control. Reporting
// every file as read more than one way would make the qualifier meaningless in
// the way an always-degraded check is a check nobody reads.
func TestHasInvisibleRunesLeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name    string
		content string
		want    bool
	}{
		{"plain ascii", "package main\n\nfunc main() {}\n", false},
		{"tabs and newlines", "a\tb\r\nc\n", false},
		{"ordinary unicode prose", "naive cafe 日本語 emoji \U0001f642\n", false},
		{"a leading byte order mark", "\ufeff# Title\n", true},
		{"a zero width space", "id = a\u200bb\n", true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			if got := leakscan.HasInvisibleRunes(probe.content); got != probe.want {
				t.Fatalf("HasInvisibleRunes = %v, want %v", got, probe.want)
			}
		})
	}
}

// TestABomDoesNotMakeAFileBinary keeps the two decisions separate. A byte order
// mark is ordinary in a text file, and calling it binary would report reduced
// coverage on files that were read perfectly well, which is the same kind of
// dishonest check line the round-4 fix removed from the other side.
func TestABomDoesNotMakeAFileBinary(t *testing.T) {
	t.Parallel()

	if leakscan.IsBinaryContent("\ufeff# Title\n\nplain prose\n") {
		t.Fatal("a byte order mark made an ordinary text file binary")
	}
}
