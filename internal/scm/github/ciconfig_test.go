package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/scm"
)

func workflowContentResponse(content string) string {
	return fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(content)))
}

func TestProbeCIConfigurationExaminesWorkflowTriggers(t *testing.T) {
	t.Parallel()
	const list = `[{"name":"ci.yml","path":".github/workflows/ci.yml","type":"file"}]`
	cases := []struct {
		name     string
		workflow string
		branch   string
		want     scm.CIConfiguration
	}{
		{"pull request", "on: pull_request\njobs: {}\n", "feature/topic", scm.CIConfigurationPresent},
		{"matching push branch", "on:\n  push:\n    branches: ['feature/**']\njobs: {}\n", "feature/topic", scm.CIConfigurationPresent},
		{"default-only push", "on:\n  push:\n    branches: [main]\njobs: {}\n", "feature/topic", scm.CIConfigurationAbsent},
		{"manual only", "on: workflow_dispatch\njobs: {}\n", "feature/topic", scm.CIConfigurationAbsent},
		{"schedule only", "on:\n  schedule:\n    - cron: '0 0 * * *'\njobs: {}\n", "feature/topic", scm.CIConfigurationAbsent},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host := New(githubTestCmdFactory(map[string]githubTestResponse{
				"gh api repos/test/repo/contents/.github/workflows?ref=abc123":        {stdout: list},
				"gh api repos/test/repo/contents/.github/workflows/ci.yml?ref=abc123": {stdout: workflowContentResponse(tc.workflow)},
			}), nil, "", "test/repo")
			got, err := host.ProbeCIConfiguration(context.Background(), &scm.PR{Number: "42"}, tc.branch, "main", "abc123")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ProbeCIConfiguration() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProbeCIConfigurationFailsClosedWhenWorkflowUnreadable(t *testing.T) {
	t.Parallel()
	host := New(githubTestCmdFactory(map[string]githubTestResponse{
		"gh api repos/test/repo/contents/.github/workflows?ref=abc123": {
			stdout: `[{"name":"ci.yml","path":".github/workflows/ci.yml","type":"file"}]`,
		},
		"gh api repos/test/repo/contents/.github/workflows/ci.yml?ref=abc123": {stderr: "HTTP 403", code: 1},
	}), nil, "", "test/repo")
	got, err := host.ProbeCIConfiguration(context.Background(), &scm.PR{Number: "42"}, "feature/topic", "main", "abc123")
	if err == nil || got != scm.CIConfigurationUnknown {
		t.Fatalf("got (%q, %v), want unknown error", got, err)
	}
}
