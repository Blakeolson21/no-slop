package risk_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

// The round-3 review defeated both structural invariant classes with one flag.
// `--tier leak-scan-only` lowered any tier provenance had not already raised,
// so a bare authorization weakening and a full AGENTS.md rewrite both reached
// "verdict: pass" at exit 0, and `--force-tier` did the same over a live
// escalation. These tests pin the ruling that closed it: the tier flag is
// escalate-only, and the refusal names the tier the classifier computed.

func weakenedAuthChange() risk.ChangeSet {
	return risk.ChangeSet{
		Branch:        "feature/probe",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:            "internal/auth/policy.go",
			Status:          risk.Modified,
			Added:           1,
			Deleted:         1,
			BaselineContent: "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" && mfa\n}\n",
			CurrentContent:  "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" || mfa\n}\n",
		}},
	}
}

func instructionRewriteChange() risk.ChangeSet {
	return risk.ChangeSet{
		Branch:        "feature/probe",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:            "AGENTS.md",
			Status:          risk.Modified,
			Added:           1,
			Deleted:         1,
			BaselineContent: "Agents must run the full test suite before pushing.\n",
			CurrentContent:  "Agents may skip tests when the change looks small.\n",
		}},
	}
}

func TestTierFlagCannotLowerAComputedTier(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name   string
		change risk.ChangeSet
	}{
		{name: "authorization weakening", change: weakenedAuthChange()},
		{name: "fleet instruction rewrite", change: instructionRewriteChange()},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			computed, err := risk.Classify(probe.change, risk.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if computed.Tier == risk.TierLeakScanOnly {
				t.Fatalf("probe computes %q, so it cannot demonstrate a lowering refusal", computed.Tier)
			}

			decision, err := risk.Classify(probe.change, risk.Config{OverrideTier: risk.TierLeakScanOnly})
			if err == nil {
				t.Fatalf("--tier leak-scan-only lowered %q without complaint: %+v", computed.Tier, decision)
			}
			if !strings.Contains(err.Error(), string(computed.Tier)) {
				t.Fatalf("refusal does not name the computed tier %q: %v", computed.Tier, err)
			}
			if decision.Tier != computed.Tier {
				t.Fatalf("refused decision reports tier %q, want the computed %q", decision.Tier, computed.Tier)
			}
			if !decision.OverrideRefused {
				t.Fatalf("refused decision does not record the refusal: %+v", decision)
			}
			if !strings.Contains(decision.String(), "override refused: "+string(computed.Tier)+" -> leak-scan-only") {
				t.Fatalf("printed decision hides the refusal:\n%s", decision.String())
			}
		})
	}
}

// TestForceTierCannotLowerAComputedTier removes the escape hatch that made the
// refusal advisory. --force-tier is still accepted so existing invocations do
// not become argument errors, and it still cannot buy a cheaper tier.
func TestForceTierCannotLowerAComputedTier(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(weakenedAuthChange(), risk.Config{
		OverrideTier: risk.TierLeakScanOnly,
		ForceTier:    true,
	})
	if err == nil {
		t.Fatalf("--force-tier lowered the computed tier: %+v", decision)
	}
	if !strings.Contains(err.Error(), "--force-tier no longer lowers a computed tier") {
		t.Fatalf("refusal does not say what --force-tier now does: %v", err)
	}
}

// TestForceTierCannotLowerAProvenanceEscalation keeps the narrower case the
// previous rule did cover from regressing while the broader one is closed.
func TestForceTierCannotLowerAProvenanceEscalation(t *testing.T) {
	t.Parallel()

	history := escalatingHistory{}
	change := risk.ChangeSet{
		Branch:        "docs/readme",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}
	config := risk.Config{
		ProvenanceStore: history,
		AgentLaneID:     "lane-a",
		Model:           "model-x",
		OverrideTier:    risk.TierLeakScanOnly,
		ForceTier:       true,
	}
	decision, err := risk.Classify(change, config)
	if err == nil {
		t.Fatalf("--force-tier cleared a live escalation: %+v", decision)
	}
	if !strings.Contains(err.Error(), "provenance escalation") {
		t.Fatalf("refusal does not name the escalation: %v", err)
	}
}

// TestTierFlagStillRaises keeps the flag useful. Escalate-only is a direction,
// not a removal.
func TestTierFlagStillRaises(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "docs/readme",
		DefaultBranch: "main",
		Files:         []risk.FileChange{{Path: "README.md", Status: risk.Modified, Added: 1, Deleted: 1}},
	}, risk.Config{OverrideTier: risk.TierFullAdversarial})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial || !decision.Overridden {
		t.Fatalf("decision = %+v, want the raise applied", decision)
	}
}

// escalatingHistory is a lane with three accepted test-capitulation findings,
// which is exactly the threshold conditionOnProvenance escalates on.
type escalatingHistory struct{}

func (escalatingHistory) Window(string, string) ([]provenance.Record, error) {
	records := make([]provenance.Record, 3)
	for index := range records {
		records[index] = provenance.Record{
			ChangeID: "unknown",
			FindingsByLens: map[string]provenance.LensFindings{
				"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
			},
		}
	}
	return records, nil
}

func (escalatingHistory) HasIdentifiedHistory() (bool, error) { return true, nil }
