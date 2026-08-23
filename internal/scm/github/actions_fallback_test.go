package github

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/scm"
)

func fallbackResponses(baseBranch string, requiredAppID int64) map[string]githubTestResponse {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	graphql := "gh api graphql -f query=" + commitChecksQuery + " -F owner=test -F name=repo -F oid=" + sha
	requiredPath := "repos/test/repo/branches/" + baseBranch + "/protection/required_status_checks"
	return map[string]githubTestResponse{
		"gh pr view 123 --repo test/repo --json headRefOid --jq .headRefOid": {stdout: sha + "\n"},
		graphql: {stderr: "GraphQL: Resource not accessible by integration (HTTP 403)", code: 1},
		"gh api --method GET repos/test/repo/actions/runs -f head_sha=" + sha + " -f per_page=100 --paginate --slurp": {
			stdout: `[{"total_count":1,"workflow_runs":[{"id":77,"name":"CI","head_sha":"` + sha + `","run_attempt":1,"status":"completed","conclusion":"success","updated_at":"2026-08-23T00:00:00Z","html_url":"https://github.com/test/repo/actions/runs/77"}]}]`,
		},
		"gh api --method GET repos/test/repo/actions/runs/77/jobs -f filter=latest -f per_page=100 --paginate --slurp": {
			stdout: `[{"total_count":1,"jobs":[{"id":88,"run_id":77,"run_attempt":1,"head_sha":"` + sha + `","name":"build","status":"completed","conclusion":"success","completed_at":"2026-08-23T00:00:00Z","html_url":"https://github.com/test/repo/actions/runs/77/job/88"}]}]`,
		},
		"gh pr view 123 --repo test/repo --json baseRefName --jq .baseRefName": {stdout: "release/v1\n"},
		"gh api --method GET " + requiredPath: {
			stdout: fmt.Sprintf(`{"checks":[{"context":"build","app_id":%d}]}`, requiredAppID),
		},
	}
}

func TestGetChecksFallsBackToActionsAtExactHead(t *testing.T) {
	t.Parallel()
	host := New(githubTestCmdFactory(fallbackResponses("release%2Fv1", githubActionsAppID)), nil, "", "test/repo")
	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Name != "build" || checks[0].Bucket != scm.CheckBucketPass {
		t.Fatalf("checks = %+v, want exact-head green build", checks)
	}
}

func TestGetChecksFallbackRejectsSameNameFromWrongApp(t *testing.T) {
	t.Parallel()
	host := New(githubTestCmdFactory(fallbackResponses("release%2Fv1", 99999)), nil, "", "test/repo")
	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "seed"})
	if err == nil || !errors.Is(err, ErrActionsEvidenceMissing) {
		t.Fatalf("checks=%+v err=%v, want unavailable evidence for app-bound mismatch", checks, err)
	}
}

func TestGetChecksDoesNotFallbackOnGeneralRollupFailure(t *testing.T) {
	t.Parallel()
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	graphql := "gh api graphql -f query=" + commitChecksQuery + " -F owner=test -F name=repo -F oid=" + sha
	host := New(githubTestCmdFactory(map[string]githubTestResponse{
		"gh pr view 123 --repo test/repo --json headRefOid --jq .headRefOid": {stdout: sha + "\n"},
		graphql: {stderr: "HTTP 502 upstream unavailable", code: 1},
	}), nil, "", "test/repo")
	_, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "seed"})
	if err == nil || errors.Is(err, ErrRollupUnavailable) {
		t.Fatalf("err=%v, want original non-capability failure", err)
	}
}
