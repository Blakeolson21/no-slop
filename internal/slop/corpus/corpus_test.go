package corpus_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/corpus"
	"github.com/Blakeolson21/no-slop/internal/slop/lenses"
	"github.com/Blakeolson21/no-slop/internal/slop/prose"
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

func TestLoadCaseSetSelectsAnExplicitHistoricalSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "round-four.json")
	caseJSON := `{"schema_version":1,"id":"case-a","description":"case a","expected_findings":[]}`
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	caseDir := filepath.Join(root, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(caseJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "change.diff"), []byte(diff), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := corpus.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	digest := historicalCaseSetDigest("case-a", []byte(caseJSON), []byte(diff))
	manifest := fmt.Sprintf(`{"schema_version":1,"name":"round-four","content_sha256":"%x","case_ids":["case-a"]}`, digest)
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	selected, err := corpus.LoadCaseSet(path, cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != "case-a" {
		t.Fatalf("selected cases = %+v, want case-a", selected)
	}

	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(caseJSON+diff[:1]), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "change.diff"), []byte(diff[1:]), 0o644); err != nil {
		t.Fatal(err)
	}
	shifted, err := corpus.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.LoadCaseSet(path, shifted); err == nil || !strings.Contains(err.Error(), "content SHA-256") {
		t.Fatalf("boundary-shifted content error = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"schema_version":1,"name":"round-four","content_sha256":"0000000000000000000000000000000000000000000000000000000000000000","case_ids":["missing"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.LoadCaseSet(path, cases); err == nil || !strings.Contains(err.Error(), `unknown case "missing"`) {
		t.Fatalf("unknown case error = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"schema_version":1,"name":"round-four","content_sha256":"0000000000000000000000000000000000000000000000000000000000000000","case_ids":["case-a"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.LoadCaseSet(path, cases); err == nil || !strings.Contains(err.Error(), "content SHA-256") {
		t.Fatalf("changed content error = %v", err)
	}
}

func historicalCaseSetDigest(id string, caseJSON, diff []byte) [sha256.Size]byte {
	var snapshot bytes.Buffer
	writeHistoricalFrame(&snapshot, caseJSON)
	writeHistoricalFrame(&snapshot, diff)
	var aggregate bytes.Buffer
	writeHistoricalFrame(&aggregate, []byte(id))
	writeHistoricalFrame(&aggregate, snapshot.Bytes())
	return sha256.Sum256(aggregate.Bytes())
}

func writeHistoricalFrame(buffer *bytes.Buffer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	buffer.Write(size[:])
	buffer.Write(value)
}

func TestHistoricalCaseSetReplaysBaselineAndRoundFourCaptures(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus")
	cases, err := corpus.Load(filepath.Join(root, "seeds"))
	if err != nil {
		t.Fatal(err)
	}
	cases, err = corpus.LoadCaseSet(filepath.Join(root, "case-sets", "rounds-1-through-4.json"), cases)
	if err != nil {
		t.Fatal(err)
	}
	for _, capture := range []struct {
		name          string
		found         int
		missed        int
		falsePositive int
	}{
		{name: "2026-08-12", found: 10, missed: 22},
		{name: "2026-08-12-r4", found: 32},
	} {
		unconditioned, err := corpus.LoadResults(filepath.Join(root, "results", capture.name, "unconditioned.json"))
		if err != nil {
			t.Fatal(err)
		}
		conditioned, err := corpus.LoadResults(filepath.Join(root, "results", capture.name, "conditioned.json"))
		if err != nil {
			t.Fatal(err)
		}
		comparison, err := corpus.Compare(cases, unconditioned, conditioned)
		if err != nil {
			t.Fatalf("replay %s: %v", capture.name, err)
		}
		for _, score := range []corpus.Score{comparison.Unconditioned, comparison.Conditioned} {
			if score.Found != capture.found || score.Missed != capture.missed || score.FalsePositive != capture.falsePositive {
				t.Fatalf("replay %s score = %+v, want found %d, missed %d, false-positive %d", capture.name, score, capture.found, capture.missed, capture.falsePositive)
			}
		}
	}
}

// The case-set manifest pins a SHA-256 over the exact bytes of each recorded
// case, so the pin only holds if a checkout reproduces the committed bytes.
// core.autocrlf=true is the Git for Windows default, and under it every corpus
// text file lands with CRLF line endings, which digests differently and failed
// the pinned replay on the Windows CI leg alone. Drive the real consumer: check
// the corpus out through Git with that setting and load the pinned case set.
func TestPinnedCaseSetSurvivesAnAutoCRLFCheckout(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..", "..")
	checkout := t.TempDir()

	gitCheckoutRun(t, checkout, "init", "-b", "main", ".")
	gitCheckoutRun(t, checkout, "config", "core.autocrlf", "true")
	gitCheckoutRun(t, checkout, "config", "user.email", "test@example.com")
	gitCheckoutRun(t, checkout, "config", "user.name", "Test")

	for _, relative := range []string{
		".gitattributes",
		filepath.Join("corpus", "seeds"),
		filepath.Join("corpus", "case-sets"),
	} {
		copyIntoCheckout(t, filepath.Join(repoRoot, relative), filepath.Join(checkout, relative))
	}
	gitCheckoutRun(t, checkout, "add", "-A")
	gitCheckoutRun(t, checkout, "commit", "-m", "corpus")

	// Discard the worktree copies and let Git materialize them itself, so the
	// bytes under test are the ones a fresh clone on Windows would receive.
	if err := os.RemoveAll(filepath.Join(checkout, "corpus")); err != nil {
		t.Fatal(err)
	}
	gitCheckoutRun(t, checkout, "checkout", "--", ".")

	cases, err := corpus.Load(filepath.Join(checkout, "corpus", "seeds"))
	if err != nil {
		t.Fatalf("load seeds from an autocrlf checkout: %v", err)
	}
	selected, err := corpus.LoadCaseSet(filepath.Join(checkout, "corpus", "case-sets", "rounds-1-through-4.json"), cases)
	if err != nil {
		t.Fatalf("load pinned case set from an autocrlf checkout: %v", err)
	}
	if len(selected) == 0 {
		t.Fatal("pinned case set selected no cases")
	}
}

func gitCheckoutRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func copyIntoCheckout(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	}); err != nil {
		t.Fatal(err)
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
