package corpus_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/corpus"
	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
)

func TestCompareScoresConditionedAndUnconditionedFindings(t *testing.T) {
	t.Parallel()

	cases := []corpus.Case{
		{ID: "fail-open", ExpectedFindings: []corpus.Finding{{Lens: "fail-open-default", Path: "policy.go", Line: 8}}},
		{ID: "test-floor", ExpectedFindings: []corpus.Finding{{Lens: "test-capitulation", Path: "calc_test.go", Line: 12}}},
	}
	unconditioned := corpus.Results{Policy: "unconditioned", Cases: []corpus.CaseResult{
		{CaseID: "fail-open", Findings: []corpus.Finding{
			{Lens: "fail-open-default", Path: "policy.go", Line: 8},
			{Lens: "scope-expansion", Path: "policy.go", Line: 9},
		}},
		{CaseID: "test-floor", Findings: []corpus.Finding{}},
	}}
	conditioned := corpus.Results{Policy: "conditioned", Cases: []corpus.CaseResult{
		{CaseID: "fail-open", Findings: []corpus.Finding{{Lens: "fail-open-default", Path: "policy.go", Line: 8}}},
		{CaseID: "test-floor", Findings: []corpus.Finding{{Lens: "test-capitulation", Path: "calc_test.go", Line: 12}}},
	}}

	comparison, err := corpus.Compare(cases, unconditioned, conditioned)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Unconditioned.Found != 1 || comparison.Unconditioned.Missed != 1 || comparison.Unconditioned.FalsePositive != 1 {
		t.Fatalf("unconditioned score = %+v", comparison.Unconditioned)
	}
	if comparison.Conditioned.Found != 2 || comparison.Conditioned.Missed != 0 || comparison.Conditioned.FalsePositive != 0 {
		t.Fatalf("conditioned score = %+v", comparison.Conditioned)
	}
}

func TestLoadRequiresRecordedDiff(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caseDir := filepath.Join(root, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(`{"schema_version":1,"id":"case-a","expected_findings":[{"lens":"fail-open-default","path":"policy.go","line":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Load(root); err == nil || !strings.Contains(err.Error(), "read recorded diff") {
		t.Fatalf("missing diff error = %v, want recorded-diff branch", err)
	}
}

func TestLoadRejectsUnknownExpectedLens(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caseDir := filepath.Join(root, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(`{"schema_version":1,"id":"case-a","expected_findings":[{"lens":"not-a-lens","path":"policy.go","line":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "change.diff"), []byte("--- a/policy.go\n+++ b/policy.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Load(root); err == nil || !strings.Contains(err.Error(), `unknown expected lens "not-a-lens"`) {
		t.Fatalf("unknown lens error = %v, want lens-validation branch", err)
	}
}

func TestLoadAcceptsMandatoryFindingKindsAndPathlessThreadState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, testCase := range []struct {
		dir     string
		finding string
		path    string
	}{
		{dir: "leak", finding: "leak-identity-scan", path: "sample.env"},
		{dir: "thread", finding: "thread-closed"},
		{dir: "duplicate", finding: "duplicate-claim", path: "outbound/comment.md"},
	} {
		caseDir := filepath.Join(root, testCase.dir)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		encoded := `{"schema_version":1,"id":"` + testCase.dir + `","expected_findings":[{"lens":"` + testCase.finding + `","path":"` + testCase.path + `","line":0}]}`
		if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(encoded), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(caseDir, "change.diff"), []byte("--- a/file\n+++ b/file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := corpus.Load(root); err != nil {
		t.Fatalf("load mandatory finding cases: %v", err)
	}
}

func TestReplayMandatoryChecksUsesDiffAndThreadFixtureWithoutExpectations(t *testing.T) {
	t.Parallel()

	diff := []byte("diff --git a/sample.env b/sample.env\nnew file mode 100644\n--- /dev/null\n+++ b/sample.env\n@@ -0,0 +1 @@\n+api_key=\"EXAMPLE_NOT_A_REAL_KEY_123456789\"\n" +
		"diff --git a/outbound/comment.md b/outbound/comment.md\nnew file mode 100644\n--- /dev/null\n+++ b/outbound/comment.md\n@@ -0,0 +1 @@\n+Please review the timeout fallback.\n" +
		"diff --git a/calc_test.go b/calc_test.go\n--- a/calc_test.go\n+++ b/calc_test.go\n@@ -12,3 +11,0 @@\n-func TestNegative(t *testing.T) {\n-\tt.Fatal(\"negative\")\n-}\n")
	findings, err := corpus.ReplayMandatory(context.Background(), diff, corpus.ReplayOptions{Thread: &prose.Thread{Open: false}})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]corpus.Finding)
	for _, finding := range findings {
		seen[finding.Lens] = finding
	}
	if finding := seen["leak-identity-scan"]; finding.Path != "sample.env" || finding.Line != 1 {
		t.Fatalf("leak finding = %+v", finding)
	}
	if finding := seen["thread-closed"]; finding.Path != "" || finding.Line != 0 {
		t.Fatalf("thread finding = %+v", finding)
	}
	if finding := seen["test-capitulation"]; finding.Path != "calc_test.go" || finding.Line != 12 {
		t.Fatalf("test-count finding = %+v", finding)
	}
}

func TestSeedCorpusCoversEachTaxonomyLens(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus", "seeds")
	cases, err := corpus.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("corpus has %d cases, want at least 30", len(cases))
	}
	seen := make(map[string]int)
	for _, testCase := range cases {
		for _, finding := range testCase.ExpectedFindings {
			seen[finding.Lens]++
		}
	}
	for _, lens := range lenses.Names() {
		if seen[lens] < 3 {
			t.Errorf("lens %q has %d corpus cases, want at least 3", lens, seen[lens])
		}
	}
	for finding, minimum := range map[string]int{
		"leak-identity-scan": 4,
		"thread-closed":      2,
		"duplicate-claim":    2,
	} {
		if seen[finding] < minimum {
			t.Errorf("finding class %q has %d corpus cases, want at least %d", finding, seen[finding], minimum)
		}
	}
}

func TestReplayMandatoryChecksMatchEverySeedWithoutExtras(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus")
	cases, err := corpus.Load(filepath.Join(root, "seeds"))
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "campaign.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []struct {
			CaseID        string        `json:"case_id"`
			Intent        string        `json:"intent"`
			ThreadFixture *prose.Thread `json:"thread_fixture"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	inputs := make(map[string]struct {
		intent string
		thread *prose.Thread
	}, len(manifest.Cases))
	for _, item := range manifest.Cases {
		inputs[item.CaseID] = struct {
			intent string
			thread *prose.Thread
		}{intent: item.Intent, thread: item.ThreadFixture}
	}

	results := corpus.Results{SchemaVersion: corpus.CurrentSchemaVersion, Policy: "mechanical-regression"}
	for _, testCase := range cases {
		input, ok := inputs[testCase.ID]
		if !ok {
			t.Fatalf("campaign is missing corpus case %q", testCase.ID)
		}
		findings, err := corpus.ReplayMandatory(context.Background(), testCase.Diff, corpus.ReplayOptions{Intent: input.intent, Thread: input.thread})
		if err != nil {
			t.Fatalf("replay %s: %v", testCase.ID, err)
		}
		results.Cases = append(results.Cases, corpus.CaseResult{CaseID: testCase.ID, Findings: findings})
	}

	comparison, err := corpus.Compare(cases, results, results)
	if err != nil {
		t.Fatal(err)
	}
	score := comparison.Unconditioned
	if score.Found != 35 || score.Missed != 0 || score.FalsePositive != 0 {
		t.Fatalf("mechanical corpus score = found %d, missed %d, false-positive %d; want 35, 0, 0", score.Found, score.Missed, score.FalsePositive)
	}
}

func TestCampaignPoliciesCarryRoundFourDecisionRules(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus", "policies")
	for _, name := range []string{"conditioned.md", "unconditioned.md"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		policy := strings.ToLower(string(content))
		for _, decisionRule := range []string{
			"same expression on both sides",
			"numeric tolerance changed to a larger threshold",
			"independent literal or standard vector",
			"nearby permissive returns",
			"compare every new file",
			"approval reference",
			"privileged objects returned",
			"versioned routes",
		} {
			if !strings.Contains(policy, decisionRule) {
				t.Errorf("%s is missing decision rule %q", name, decisionRule)
			}
		}
	}
}
