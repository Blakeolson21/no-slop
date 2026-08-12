package corpus_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/corpus"
	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
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

func TestLoadRequiresRecordedDiffAndKnownExpectedLens(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caseDir := filepath.Join(root, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(`{"schema_version":1,"id":"case-a","expected_findings":[{"lens":"not-a-lens","path":"policy.go","line":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Load(root); err == nil {
		t.Fatal("expected missing diff and unknown lens to fail corpus loading")
	}
}

func TestSeedCorpusCoversEachTaxonomyLens(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus", "seeds")
	cases, err := corpus.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]int)
	for _, testCase := range cases {
		for _, finding := range testCase.ExpectedFindings {
			seen[finding.Lens]++
		}
	}
	for _, lens := range lenses.Names() {
		if seen[lens] != 1 {
			t.Errorf("lens %q has %d seed cases, want 1", lens, seen[lens])
		}
	}
}
