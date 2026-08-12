package risk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/provenance"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

type historyReader struct {
	records []provenance.Record
	err     error
}

func (r historyReader) Recent(string, string, int) ([]provenance.Record, error) {
	return r.records, r.err
}

func TestClassifyMarkdownOnlyChangeUsesLeakScanOnlyTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/readme-refresh",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:    "docs/guide.md",
			Status:  risk.Modified,
			Added:   8,
			Deleted: 3,
		}},
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierLeakScanOnly {
		t.Fatalf("tier = %q, want %q", decision.Tier, risk.TierLeakScanOnly)
	}

	printed := decision.String()
	for _, want := range []string{
		"tier: leak-scan-only",
		"blast radius:",
		"novelty:",
		"reversibility:",
		"Markdown-only",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed decision missing %q:\n%s", want, printed)
		}
	}
}

func TestClassifyOrdinarySourceChangeUsesSingleReviewTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/cache-key",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:    "internal/cache/key.go",
			Status:  risk.Modified,
			Added:   18,
			Deleted: 7,
		}},
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierSingleReview {
		t.Fatalf("tier = %q, want %q", decision.Tier, risk.TierSingleReview)
	}
	if decision.BlastRadius.Score == 0 || decision.Novelty.Score == 0 {
		t.Fatalf("ordinary source change received zero reach or novelty: %+v", decision)
	}
}

func TestClassifyHighReachNewLogicUsesFullAdversarialTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/session-policy",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:   "internal/auth/policy.go",
			Status: risk.Added,
			Added:  140,
		}},
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("tier = %q, want %q", decision.Tier, risk.TierFullAdversarial)
	}
	if decision.BlastRadius.Score < 3 {
		t.Fatalf("blast radius = %+v, want high reach", decision.BlastRadius)
	}
	if decision.Novelty.Score < 3 {
		t.Fatalf("novelty = %+v, want new logic", decision.Novelty)
	}
}

func TestClassifySubstantialOrdinarySourceAdditionUsesFullAdversarialTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/new-command",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:   "cmd/tool/main.go",
			Status: risk.Added,
			Added:  1200,
		}},
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("decision = %+v, want substantial new source to reach full review", decision)
	}
	if decision.BlastRadius.Score != 3 {
		t.Fatalf("blast radius = %+v, want substantial source reach", decision.BlastRadius)
	}
}

func TestClassifyPrintsExplicitTierOverride(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/wording",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}, risk.Config{OverrideTier: risk.TierFullAdversarial})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial || !decision.Overridden {
		t.Fatalf("decision = %+v, want explicit full override", decision)
	}
	if !strings.Contains(decision.String(), "override: leak-scan-only -> full-adversarial") {
		t.Fatalf("printed decision does not disclose override:\n%s", decision.String())
	}
}

func TestClassifyDoesNotPrintNoOpTierOverride(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/wording",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}, risk.Config{OverrideTier: risk.TierLeakScanOnly})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Overridden {
		t.Fatalf("decision = %+v, want matching override treated as no-op", decision)
	}
	if strings.Contains(decision.String(), "override:") {
		t.Fatalf("decision prints a no-op override:\n%s", decision.String())
	}
}

func TestClassifyTreatsConfiguredSubtreeAsHighRisk(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/worker",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "platform/workers/queue.go", Status: risk.Modified, Added: 5, Deleted: 2}},
	}, risk.Config{HighRiskPaths: []string{"platform/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlastRadius.Score != 3 || decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("decision = %+v, want configured subtree to route full", decision)
	}
}

func TestClassifyTreatsConfiguredMarkdownAsHighRiskInstructions(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/agent-policy",
		DefaultBranch: "main",
		Files: []risk.FileChange{
			{Path: "AGENTS.md", Status: risk.Modified, Added: 1, Deleted: 1},
			{Path: "skills/deploy/SKILL.md", Status: risk.Modified, Added: 1, Deleted: 1},
		},
	}, risk.Config{HighRiskPaths: []string{"AGENTS.md", "skills/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("tier = %q, want %q: %+v", decision.Tier, risk.TierFullAdversarial, decision)
	}
	if decision.BlastRadius.Score != 3 || decision.Novelty.Score != 2 {
		t.Fatalf("axes = blast %+v novelty %+v, want high-risk instruction change", decision.BlastRadius, decision.Novelty)
	}
	if strings.Contains(decision.String(), "do not reach runtime code") {
		t.Fatalf("decision states a false Markdown shortcut:\n%s", decision.String())
	}
}

func TestClassifyConsistentIdentifierRenameAsMechanical(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "refactor/cache-name",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:            "internal/cache/key.go",
			Status:          risk.Modified,
			Added:           2,
			Deleted:         2,
			BaselineContent: "package cache\nfunc key(input string) string { return input }\n",
			CurrentContent:  "package cache\nfunc cacheKey(value string) string { return value }\n",
		}},
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Novelty.Score != 0 || !strings.Contains(decision.Novelty.Reason, "identifier renames") {
		t.Fatalf("novelty = %+v, want mechanical source edit", decision.Novelty)
	}
}

func TestClassifyCallTargetAndArgumentSwapAsChangedLogic(t *testing.T) {
	t.Parallel()

	baseline := "package policy\nfunc allowed(actor, resource string) bool { return strings.HasPrefix(actor, \"admin:\") && owns(actor, resource) }\n"
	for name, current := range map[string]string{
		"call target":   "package policy\nfunc allowed(actor, resource string) bool { return strings.Contains(actor, \"admin:\") && owns(actor, resource) }\n",
		"argument swap": "package policy\nfunc allowed(actor, resource string) bool { return strings.HasPrefix(actor, \"admin:\") && owns(resource, actor) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := risk.Classify(risk.ChangeSet{
				Branch:        "feature/authorization-policy",
				DefaultBranch: "main",
				Files: []risk.FileChange{{
					Path:            "policy.go",
					Status:          risk.Modified,
					Added:           1,
					Deleted:         1,
					BaselineContent: baseline,
					CurrentContent:  current,
				}},
			}, risk.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Novelty.Score != 2 {
				t.Fatalf("novelty = %+v, want changed logic", decision.Novelty)
			}
			if decision.Tier == risk.TierLeakScanOnly {
				t.Fatalf("tier = %q, want reviewer tier", decision.Tier)
			}
		})
	}
}

func TestClassifyLiteralOrOperatorChangeAsLogic(t *testing.T) {
	t.Parallel()

	for _, current := range []string{
		"package cache\nfunc key(input int) int { return input - 1 }\n",
		"package cache\nfunc key(input int) int { return input + 2 }\n",
	} {
		decision, err := risk.Classify(risk.ChangeSet{
			Branch:        "feature/cache-key",
			DefaultBranch: "main",
			Files: []risk.FileChange{{
				Path:            "internal/cache/key.go",
				Status:          risk.Modified,
				Added:           1,
				Deleted:         1,
				BaselineContent: "package cache\nfunc key(input int) int { return input + 1 }\n",
				CurrentContent:  current,
			}},
		}, risk.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Novelty.Score != 2 {
			t.Fatalf("novelty = %+v for %q, want changed logic", decision.Novelty, current)
		}
	}
}

func TestClassifyConditionsTierLensOrderAndProbesOnLaneModelHistory(t *testing.T) {
	t.Parallel()

	records := make([]provenance.Record, 3)
	for index := range records {
		records[index] = provenance.Record{FindingsByLens: map[string]provenance.LensFindings{
			"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
		}}
	}
	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/update",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}, risk.Config{
		ProvenanceStore: recordsReader(records),
		AgentLaneID:     "lane-x",
		Model:           "model-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierSingleReview {
		t.Fatalf("tier = %q, want one-tier escalation", decision.Tier)
	}
	if len(decision.PriorityLenses) == 0 || decision.PriorityLenses[0] != "test-capitulation" {
		t.Fatalf("priority lenses = %v", decision.PriorityLenses)
	}
	if len(decision.DeterministicProbes) != 1 || decision.DeterministicProbes[0] != "test-count-floor" {
		t.Fatalf("deterministic probes = %v", decision.DeterministicProbes)
	}
	want := "lane lane-x: 3 test-capitulation findings in last 3 changes, escalating"
	if !strings.Contains(decision.String(), want) {
		t.Fatalf("decision rationale missing %q:\n%s", want, decision.String())
	}
}

func TestClassifyWithNoLaneModelHistoryKeepsV1TierAndPrintsDefault(t *testing.T) {
	t.Parallel()

	change := risk.ChangeSet{
		Branch:        "docs/update",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}
	v1, err := risk.Classify(change, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conditioned, err := risk.Classify(change, risk.Config{
		ProvenanceStore: recordsReader(nil),
		AgentLaneID:     "lane-new",
		Model:           "model-new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conditioned.Tier != v1.Tier || conditioned.BlastRadius != v1.BlastRadius || conditioned.Novelty != v1.Novelty || conditioned.Reversibility != v1.Reversibility {
		t.Fatalf("conditioned = %+v, want v1 route %+v", conditioned, v1)
	}
	if !strings.Contains(conditioned.String(), "no history for lane lane-new and model model-new; using v1 policy") {
		t.Fatalf("decision does not print safe default:\n%s", conditioned.String())
	}
}

func TestClassifyUnreadableHistoryEscalatesToStrictestTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/update",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}, risk.Config{
		ProvenanceStore: historyReader{err: errors.New("malformed history")},
		AgentLaneID:     "lane-x",
		Model:           "model-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("decision = %+v, want strictest tier", decision)
	}
	if !strings.Contains(decision.String(), "history could not be read") || !strings.Contains(decision.String(), "escalating to full-adversarial") {
		t.Fatalf("decision does not print fail-closed rationale:\n%s", decision.String())
	}
}

func recordsReader(records []provenance.Record) historyReader {
	return historyReader{records: records}
}
