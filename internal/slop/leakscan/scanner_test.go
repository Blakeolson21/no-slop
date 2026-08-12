package leakscan_test

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/leakscan"
)

func TestScanFindsSecretShapeWithoutEchoingSecret(t *testing.T) {
	t.Parallel()

	secret := "ghp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	findings := leakscan.Scan([]leakscan.File{{
		Path:    "config/example.env",
		Content: "PUBLIC=true\nTOKEN=" + secret + "\n",
	}}, leakscan.Options{})
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one secret finding", findings)
	}
	if findings[0].Kind != leakscan.Secret || findings[0].Line != 2 {
		t.Fatalf("finding = %+v, want secret on line 2", findings[0])
	}
	if strings.Contains(findings[0].Description, secret) {
		t.Fatal("finding description echoed the detected secret")
	}
}

func TestScanUsesCaseInsensitivePrivateNameBlocklist(t *testing.T) {
	t.Parallel()

	entries := leakscan.ParseBlocklist("# one private name per line\nprivate-build-host\n\n")
	findings := leakscan.Scan([]leakscan.File{{
		Path:    "docs/post.md",
		Content: "Validated on PRIVATE-BUILD-HOST before publishing.\n",
	}}, leakscan.Options{Blocklist: entries})
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one identity finding", findings)
	}
	if findings[0].Kind != leakscan.Identity || findings[0].Line != 1 {
		t.Fatalf("finding = %+v, want identity on line 1", findings[0])
	}
	if strings.Contains(strings.ToLower(findings[0].Description), "private-build-host") {
		t.Fatal("finding description echoed the private name")
	}
}

func TestScanRecognizesCommonCredentialShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
	}{
		{"AWS access key", "AWS_ACCESS_KEY_ID=AKIA" + "IOSFODNN7EXAMPLE"},
		{"Slack token", "SLACK_TOKEN=xoxb-123456789012-" + "123456789012-abcdefghijklmnopqrstuvwx"},
		{"private key", "-----BEGIN PRIVATE" + " KEY-----"},
		{"generic assignment", "api_token: \"abcdefghijklmnopqrstuvwxyz" + "0123456789ABCDEFGH\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := leakscan.Scan([]leakscan.File{{Path: "example.txt", Content: tc.content}}, leakscan.Options{})
			if len(findings) == 0 || findings[0].Kind != leakscan.Secret {
				t.Fatalf("findings = %+v, want secret finding", findings)
			}
		})
	}
}

func TestScanFindsPersonalHomePathsWithoutEchoingIdentity(t *testing.T) {
	t.Parallel()

	privatePath := "/" + "Users/example-person/project"
	findings := leakscan.Scan([]leakscan.File{{Path: "report.md", Content: "artifact: " + privatePath}}, leakscan.Options{})
	if len(findings) != 1 || findings[0].Kind != leakscan.Identity {
		t.Fatalf("findings = %+v, want one identity finding", findings)
	}
	if strings.Contains(findings[0].Description, "example-person") {
		t.Fatal("identity finding echoed the home directory owner")
	}
}
