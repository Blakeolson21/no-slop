package leakscan_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/leakscan"
)

func TestScanFindsSecretShapeWithoutEchoingSecret(t *testing.T) {
	t.Parallel()

	secret := "ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ" // noslop:allow-leak
	result := leakscan.Scan([]leakscan.File{{
		Path:    "config/example.env",
		Content: "PUBLIC=true\nTOKEN=" + secret + "\n",
	}}, leakscan.Options{})
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %+v, want one secret finding", result.Findings)
	}
	if result.Findings[0].Kind != leakscan.Secret || result.Findings[0].Line != 2 {
		t.Fatalf("finding = %+v, want secret on line 2", result.Findings[0])
	}
	if strings.Contains(result.Findings[0].Description, secret) {
		t.Fatal("finding description echoed the detected secret")
	}
}

func TestScanUsesCaseInsensitivePrivateNameBlocklist(t *testing.T) {
	t.Parallel()

	entries := leakscan.ParseBlocklist("# one private name per line\nprivate-build-host\n\n")
	result := leakscan.Scan([]leakscan.File{{
		Path:    "docs/post.md",
		Content: "Validated on PRIVATE-BUILD-HOST before publishing.\n",
	}}, leakscan.Options{Blocklist: entries})
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %+v, want one identity finding", result.Findings)
	}
	if result.Findings[0].Kind != leakscan.Identity || result.Findings[0].Line != 1 {
		t.Fatalf("finding = %+v, want identity on line 1", result.Findings[0])
	}
	if strings.Contains(strings.ToLower(result.Findings[0].Description), "private-build-host") {
		t.Fatal("finding description echoed the private name")
	}
}

func TestScanRecognizesCommonCredentialShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
	}{
		{"AWS access key", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"},                           // noslop:allow-leak
		{"Slack token", "SLACK_TOKEN=xoxb-XXXXXXXXXXXX-XXXXXXXXXXXX-XXXXXXXXXXXXXXXXXXXXXXXX"}, // noslop:allow-leak
		{"private key", "-----BEGIN PRIVATE KEY-----"},                                         // noslop:allow-leak
		{"generic assignment", "api_token: \"abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH\""},  // noslop:allow-leak
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := leakscan.Scan([]leakscan.File{{Path: "example.txt", Content: tc.content}}, leakscan.Options{})
			if len(result.Findings) == 0 || result.Findings[0].Kind != leakscan.Secret {
				t.Fatalf("findings = %+v, want secret finding", result.Findings)
			}
		})
	}
}

func TestScanFindsPersonalHomePathsWithoutEchoingIdentity(t *testing.T) {
	t.Parallel()

	privatePath := "/Users/example-person/project" // noslop:allow-leak
	result := leakscan.Scan([]leakscan.File{{Path: "report.md", Content: "artifact: " + privatePath}}, leakscan.Options{})
	if len(result.Findings) != 1 || result.Findings[0].Kind != leakscan.Identity {
		t.Fatalf("findings = %+v, want one identity finding", result.Findings)
	}
	if strings.Contains(result.Findings[0].Description, "example-person") {
		t.Fatal("identity finding echoed the home directory owner")
	}
}

func TestScanHonorsExplicitInlineExemptionOnOneLine(t *testing.T) {
	t.Parallel()

	result := leakscan.Scan([]leakscan.File{{
		Path: "fixtures/tokens.txt",
		Content: "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\n" + // noslop:allow-leak
			"TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\n", // noslop:allow-leak
	}}, leakscan.Options{})
	if len(result.Findings) != 1 || result.Findings[0].Line != 2 {
		t.Fatalf("findings = %+v, want only the unexempted second line", result.Findings)
	}
	if len(result.Exemptions) != 1 || result.Exemptions[0].Line != 1 || result.Exemptions[0].Marker != leakscan.InlineExemption {
		t.Fatalf("exemptions = %+v, want the marked first line reported", result.Exemptions)
	}
}

// TestIsBinaryContentHasNoSniffWindow is T3 at the unit level. Git samples the
// first 8000 bytes of a blob, and the engine's fallback keyed on whether git
// produced hunks, so a NUL past that offset read as ordinary text on both sides
// at once: the renderings never ran, the credential regex failed across the NUL,
// and the mandatory leak scan printed "completed (0 findings)" over a live AWS
// key at exit 0. The scanner's own answer has no window in it.
func TestIsBinaryContentHasNoSniffWindow(t *testing.T) {
	t.Parallel()

	prose := strings.Repeat("ordinary documentation prose here\n", 280)
	if len(prose) <= 8000 {
		t.Fatalf("the fixture is %d bytes, which does not reach past git's sniff window", len(prose))
	}
	for _, probe := range []struct {
		name    string
		content string
		want    bool
	}{
		{"plain text", "hello\nworld\n", false},
		{"text with tabs and carriage returns", "a\tb\r\nc\n", false},
		{"NUL at the front", "\x00hello\n", true},
		{"NUL past git's sniff window", prose + "key = AKIA\x00IOSFODNN7EXAMPLE\n", true},
		{"an escape byte", "a\x1bb\n", true},
	} {
		if got := leakscan.IsBinaryContent(probe.content); got != probe.want {
			t.Errorf("%s: IsBinaryContent = %v, want %v", probe.name, got, probe.want)
		}
	}
}

// TestNulSplitCredentialIsRecoveredByBothRenderings pins that the scanner reads
// the bytes both ways: NUL removed joins a credential a control byte was
// splitting, and NUL as a boundary keeps every other line's numbering intact.
func TestNulSplitCredentialIsRecoveredByBothRenderings(t *testing.T) {
	t.Parallel()

	prose := strings.Repeat("ordinary documentation prose here\n", 280)
	result := leakscan.Scan([]leakscan.File{{
		Path:    "docs/notes.txt",
		Content: prose + "aws example key = AKIA\x00IOSFODNN7EXAMPLE\n",
		Binary:  true,
	}}, leakscan.Options{})
	if len(result.Findings) == 0 {
		t.Fatal("a NUL past git's sniff window carried a live credential past the scan")
	}
}
