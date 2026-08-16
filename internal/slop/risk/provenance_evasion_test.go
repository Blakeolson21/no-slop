package risk_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

// countingHistory lets a test say separately what the lane history holds and
// whether the store has identified history at all.
type countingHistory struct {
	records    []provenance.Record
	identified bool
	err        error
}

func (h countingHistory) Recent(string, string, int) ([]provenance.Record, error) {
	return h.records, h.err
}

func (h countingHistory) HasIdentifiedHistory() (bool, error) {
	return h.identified, h.err
}

func lensRecord(accepted, rejected int) provenance.Record {
	lens := provenance.LensFindings{}
	for index := 0; index < accepted; index++ {
		lens.Accepted = append(lens.Accepted, provenance.Finding{Description: "test removed"})
	}
	for index := 0; index < rejected; index++ {
		lens.Rejected = append(lens.Rejected, provenance.Finding{Description: "disputed"})
	}
	return provenance.Record{
		SchemaVersion:  provenance.CurrentSchemaVersion,
		RecordedAt:     time.Unix(1, 0),
		AgentLaneID:    "lane-a",
		Model:          "model-x",
		FindingsByLens: map[string]provenance.LensFindings{"test-capitulation": lens},
	}
}

func sourceChange() risk.ChangeSet {
	return risk.ChangeSet{
		Branch:        "feature/probe",
		DefaultBranch: "main",
		Files: []risk.FileChange{{
			Path:            "service/handler.go",
			Status:          risk.Modified,
			BaselineContent: "package service\n\nfunc Handle() bool { return false }\n",
			CurrentContent:  "package service\n\nfunc Handle() bool { return true }\n",
		}},
	}
}

// TestOmittingTheLaneKeyEscalatesWhereIdentitiesExist closes the cheapest
// evasion. Leaving --lane-id off printed the same line an honest first-time
// lane prints, so the output could not tell the two apart.
func TestOmittingTheLaneKeyEscalatesWhereIdentitiesExist(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(sourceChange(), risk.Config{
		ProvenanceStore: countingHistory{identified: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial || !decision.ProvenanceEscalated {
		t.Fatalf("tier = %q escalated = %t, want full-adversarial", decision.Tier, decision.ProvenanceEscalated)
	}
	if !strings.Contains(decision.Rationale, "records them") {
		t.Fatalf("rationale = %q, want it to name the reason", decision.Rationale)
	}
}

// TestOmittingTheLaneKeyIsFreeWhereNoIdentitiesExist is the control. A
// repository that never supplied identities has no history to hide from, and
// must not pay the full tier for a mechanism it never opted into.
func TestOmittingTheLaneKeyIsFreeWhereNoIdentitiesExist(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(sourceChange(), risk.Config{
		ProvenanceStore: countingHistory{identified: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ProvenanceEscalated {
		t.Fatalf("escalated with no identified history: %q", decision.Rationale)
	}
}

// TestUnreadableStoreEscalatesEvenWithoutALaneKey keeps the undeterminable case
// failing closed on the identity-free path too.
func TestUnreadableStoreEscalatesEvenWithoutALaneKey(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(sourceChange(), risk.Config{
		ProvenanceStore: countingHistory{err: errors.New("malformed history")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != risk.TierFullAdversarial {
		t.Fatalf("tier = %q, want full-adversarial for unreadable history", decision.Tier)
	}
}

// TestForgedRejectionsCannotCancelAnEscalation covers the accounting evasion.
// Scoring accepted minus rejected let a record with three rejected findings
// cancel a real record with three accepted ones, so history could be cleared by
// writing more history.
func TestForgedRejectionsCannotCancelAnEscalation(t *testing.T) {
	t.Parallel()

	decision, err := risk.Classify(sourceChange(), risk.Config{
		AgentLaneID:     "lane-a",
		Model:           "model-x",
		ProvenanceStore: countingHistory{identified: true, records: []provenance.Record{lensRecord(3, 0), lensRecord(0, 3)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.ProvenanceEscalated {
		t.Fatalf("a forged rejection cancelled the escalation: %q", decision.Rationale)
	}
}
