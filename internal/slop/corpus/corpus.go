// Package corpus loads recorded review cases and scores captured policy
// findings against independent expectations.
package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
)

const (
	// CurrentSchemaVersion is the supported corpus and results format.
	CurrentSchemaVersion = 1
	caseFileName         = "case.json"
	diffFileName         = "change.diff"
)

// Finding is the stable match key used by expected and observed results.
type Finding struct {
	Lens        string `json:"lens"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Description string `json:"description,omitempty"`
}

// Case is one recorded diff and its independently stated expectations.
type Case struct {
	SchemaVersion    int       `json:"schema_version"`
	ID               string    `json:"id"`
	Description      string    `json:"description"`
	ExpectedFindings []Finding `json:"expected_findings"`
	Diff             []byte    `json:"-"`
	Directory        string    `json:"-"`
}

// CaseResult contains one policy's findings for one corpus case.
type CaseResult struct {
	CaseID   string    `json:"case_id"`
	Findings []Finding `json:"findings"`
}

// Results is a replayable capture produced by one policy.
type Results struct {
	SchemaVersion int          `json:"schema_version"`
	Policy        string       `json:"policy"`
	Cases         []CaseResult `json:"cases"`
}

// Score reports policy yield against the corpus expectations.
type Score struct {
	Policy        string
	Found         int
	Missed        int
	FalsePositive int
}

// Comparison holds side-by-side unconditioned and conditioned scores.
type Comparison struct {
	Unconditioned Score
	Conditioned   Score
}

// Load reads every immediate case directory in stable name order.
func Load(root string) ([]Case, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}
	knownLenses := make(map[string]bool)
	for _, name := range lenses.Names() {
		knownLenses[name] = true
	}
	var cases []Case
	seenIDs := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		encoded, err := os.ReadFile(filepath.Join(dir, caseFileName))
		if err != nil {
			return nil, fmt.Errorf("load corpus case %q: %w", entry.Name(), err)
		}
		var testCase Case
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&testCase); err != nil {
			return nil, fmt.Errorf("load corpus case %q: decode: %w", entry.Name(), err)
		}
		if testCase.SchemaVersion != CurrentSchemaVersion {
			return nil, fmt.Errorf("load corpus case %q: unsupported schema version %d", entry.Name(), testCase.SchemaVersion)
		}
		testCase.ID = strings.TrimSpace(testCase.ID)
		if testCase.ID == "" {
			return nil, fmt.Errorf("load corpus case %q: id is required", entry.Name())
		}
		if seenIDs[testCase.ID] {
			return nil, fmt.Errorf("load corpus case %q: duplicate id %q", entry.Name(), testCase.ID)
		}
		seenIDs[testCase.ID] = true
		if len(testCase.ExpectedFindings) == 0 {
			return nil, fmt.Errorf("load corpus case %q: expected_findings must not be empty", entry.Name())
		}
		for _, finding := range testCase.ExpectedFindings {
			if !knownLenses[finding.Lens] {
				return nil, fmt.Errorf("load corpus case %q: unknown expected lens %q", entry.Name(), finding.Lens)
			}
			if strings.TrimSpace(finding.Path) == "" {
				return nil, fmt.Errorf("load corpus case %q: expected finding path is required", entry.Name())
			}
		}
		testCase.Diff, err = os.ReadFile(filepath.Join(dir, diffFileName))
		if err != nil {
			return nil, fmt.Errorf("load corpus case %q: read recorded diff: %w", entry.Name(), err)
		}
		if len(bytes.TrimSpace(testCase.Diff)) == 0 {
			return nil, fmt.Errorf("load corpus case %q: recorded diff is empty", entry.Name())
		}
		testCase.Directory = dir
		cases = append(cases, testCase)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("load corpus: no case directories found")
	}
	sort.Slice(cases, func(left, right int) bool { return cases[left].ID < cases[right].ID })
	return cases, nil
}

// LoadResults reads a captured policy result file.
func LoadResults(path string) (Results, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Results{}, fmt.Errorf("load policy results: %w", err)
	}
	var results Results
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&results); err != nil {
		return Results{}, fmt.Errorf("load policy results: decode: %w", err)
	}
	if results.SchemaVersion != CurrentSchemaVersion {
		return Results{}, fmt.Errorf("load policy results: unsupported schema version %d", results.SchemaVersion)
	}
	if strings.TrimSpace(results.Policy) == "" {
		return Results{}, fmt.Errorf("load policy results: policy is required")
	}
	return results, nil
}

// Compare scores captured conditioned and unconditioned policy findings.
func Compare(cases []Case, unconditioned, conditioned Results) (Comparison, error) {
	unconditionedScore, err := score(cases, unconditioned)
	if err != nil {
		return Comparison{}, fmt.Errorf("score unconditioned policy: %w", err)
	}
	conditionedScore, err := score(cases, conditioned)
	if err != nil {
		return Comparison{}, fmt.Errorf("score conditioned policy: %w", err)
	}
	return Comparison{Unconditioned: unconditionedScore, Conditioned: conditionedScore}, nil
}

func score(cases []Case, results Results) (Score, error) {
	byID := make(map[string]CaseResult, len(results.Cases))
	knownCases := make(map[string]bool, len(cases))
	for _, testCase := range cases {
		knownCases[testCase.ID] = true
	}
	for _, result := range results.Cases {
		if !knownCases[result.CaseID] {
			return Score{}, fmt.Errorf("results contain unknown case %q", result.CaseID)
		}
		if _, duplicate := byID[result.CaseID]; duplicate {
			return Score{}, fmt.Errorf("results contain duplicate case %q", result.CaseID)
		}
		byID[result.CaseID] = result
	}
	result := Score{Policy: results.Policy}
	for _, testCase := range cases {
		observed, ok := byID[testCase.ID]
		if !ok {
			return Score{}, fmt.Errorf("results are missing case %q", testCase.ID)
		}
		matched := make([]bool, len(observed.Findings))
		for _, expected := range testCase.ExpectedFindings {
			found := false
			for index, actual := range observed.Findings {
				if matched[index] || !sameFinding(expected, actual) {
					continue
				}
				matched[index] = true
				found = true
				break
			}
			if found {
				result.Found++
			} else {
				result.Missed++
			}
		}
		for _, used := range matched {
			if !used {
				result.FalsePositive++
			}
		}
	}
	return result, nil
}

func sameFinding(expected, actual Finding) bool {
	if expected.Lens != actual.Lens || expected.Path != actual.Path {
		return false
	}
	return expected.Line == 0 || expected.Line == actual.Line
}
