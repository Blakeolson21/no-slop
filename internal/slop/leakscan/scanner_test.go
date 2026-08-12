package leakscan_test

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/leakscan"
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
