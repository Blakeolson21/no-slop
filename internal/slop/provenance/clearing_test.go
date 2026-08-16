package provenance_test

import (
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/slop/lenses"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// Round 5 found two ways an escalation stops costing what it is supposed to
// cost, and both are about what counts as evidence rather than about retention.
//
// U5: the first-contact escalation writes its own record. A fresh self-asserted
// lane escalates on run 1, run 1 appends a record whose outcome is "error"
// because the escalated tier could not reach a reviewer, and run 2 finds one
// record and takes the v1 route. Shedding an escalation cost one throwaway
// invocation, which restores exactly the asymmetry the fix removed: omission
// escalates forever and a fresh assertion escalates once.
//
// U6: a reviewed pass clears EVERY lens in the window, including lenses the
// change could not have exercised. In a repository where editing AGENTS.md
// reaches full-adversarial on the axes alone, a documentation change that
// passes a real review clears an accumulated test-capitulation escalation while
// containing no tests and touching no source.

func record(changeID string, mutate func(*provenance.Record)) provenance.Record {
	built := provenance.Record{
		SchemaVersion:  provenance.CurrentSchemaVersion,
		RecordedAt:     time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		ChangeID:       changeID,
		Model:          "model-x",
		AgentLaneID:    "lane-a",
		SelectedTier:   "leak-scan-only",
		FindingsByLens: map[string]provenance.LensFindings{},
		Outcome:        "fail",
	}
	mutate(&built)
	return built
}

func withAccepted(lens string, count int) func(*provenance.Record) {
	return func(r *provenance.Record) {
		findings := make([]provenance.Finding, 0, count)
		for index := 0; index < count; index++ {
			findings = append(findings, provenance.Finding{Path: "a.go", Line: index + 1, Description: "finding"})
		}
		r.FindingsByLens[lens] = provenance.LensFindings{Accepted: findings}
	}
}

// reviewedPass builds a record shaped exactly as the gate writes one for a
// clean full-adversarial run over the given content kinds.
func reviewedPass(changeID string, content ...string) provenance.Record {
	return record(changeID, func(r *provenance.Record) {
		r.SelectedTier = "full-adversarial"
		r.Outcome = "pass"
		r.Rounds = 2
		r.JudgedContent = content
	})
}

// TestAReviewedPassOnDocumentationDoesNotClearATestLens is U6. The pass is
// genuine: full-adversarial, two rounds, no findings. It is simply no evidence
// at all about how this lane writes tests, because the change it passed on
// contains none.
func TestAReviewedPassOnDocumentationDoesNotClearATestLens(t *testing.T) {
	t.Parallel()

	scores := provenance.LensScores([]provenance.Record{
		record("c1", withAccepted("test-capitulation", 3)),
		reviewedPass("c2", "docs"),
	})
	if scores["test-capitulation"] != 3 {
		t.Fatalf("test-capitulation = %d, want 3: a documentation change carries no evidence about test quality", scores["test-capitulation"])
	}
}

// TestAReviewedPassOnTestsDoesClearATestLens is the other half, and it is what
// keeps the escalation from becoming permanent. An escalation with no route out
// is the failure mode the notice severity already had to be rescued from.
func TestAReviewedPassOnTestsDoesClearATestLens(t *testing.T) {
	t.Parallel()

	scores := provenance.LensScores([]provenance.Record{
		record("c1", withAccepted("test-capitulation", 3)),
		reviewedPass("c2", "source", "tests"),
	})
	if scores["test-capitulation"] != 0 {
		t.Fatalf("test-capitulation = %d, want 0: a reviewed clean pass over tests is the evidence that clears it", scores["test-capitulation"])
	}
}

// TestAReviewedPassOnSourceDoesNotClearATestLens is the case between the two,
// and the one that says the rule is about evidence rather than about
// documentation. Source without tests says nothing about test capitulation
// either.
func TestAReviewedPassOnSourceDoesNotClearATestLens(t *testing.T) {
	t.Parallel()

	scores := provenance.LensScores([]provenance.Record{
		record("c1", withAccepted("test-capitulation", 3)),
		reviewedPass("c2", "source"),
	})
	if scores["test-capitulation"] != 3 {
		t.Fatalf("test-capitulation = %d, want 3", scores["test-capitulation"])
	}
	scores = provenance.LensScores([]provenance.Record{
		record("c1", withAccepted("fail-open-default", 3)),
		reviewedPass("c2", "source"),
	})
	if scores["fail-open-default"] != 0 {
		t.Fatalf("fail-open-default = %d, want 0: source is exactly the content that lens is found in", scores["fail-open-default"])
	}
}

// TestALegacyReviewedPassClearsNothing fails closed on the schema addition. A
// record written before the content set existed cannot say what it judged, and
// an undeterminable answer must not become the permissive one.
func TestALegacyReviewedPassClearsNothing(t *testing.T) {
	t.Parallel()

	scores := provenance.LensScores([]provenance.Record{
		record("c1", withAccepted("fail-open-default", 3)),
		reviewedPass("c2"),
	})
	if scores["fail-open-default"] != 3 {
		t.Fatalf("fail-open-default = %d, want 3: a record that does not say what it judged clears nothing", scores["fail-open-default"])
	}
}

// TestEveryCatalogLensHasAClearingRule keeps the table and the catalog from
// drifting apart silently. A lens nobody classified still has to have a route
// out, and the route it gets has to be a decision somebody made rather than a
// map lookup that missed.
//
// The membership answer is what makes that check real. Asking only whether the
// returned content set was non-empty could not fail: the any-content default
// answers three kinds for every string, including one nobody ever classified,
// so a future tests-only lens added to the catalog and forgotten here would
// have shipped clearable by a docs-only pass with this test green.
func TestEveryCatalogLensHasAClearingRule(t *testing.T) {
	t.Parallel()

	for _, lens := range lenses.Catalog() {
		content, explicit := provenance.ClearingRule(lens.Name)
		if !explicit {
			t.Errorf("lens %q is not named in the clearing table, so it silently takes the any-content default; classify it", lens.Name)
			continue
		}
		if len(content) == 0 {
			t.Errorf("lens %q has no content kind that can clear it, so its escalation would be permanent", lens.Name)
		}
	}
}

// TestAnUnclassifiedLensIsReportedAsDefaulted is the negative case that proves
// the check above can fail. A name the table does not hold is exactly what a
// newly added and unclassified catalog lens looks like, and it has to be
// distinguishable from a classified one by something other than the content
// kinds, which are identical.
func TestAnUnclassifiedLensIsReportedAsDefaulted(t *testing.T) {
	t.Parallel()

	content, explicit := provenance.ClearingRule("flaky-test-tolerance")
	if explicit {
		t.Fatalf("an unclassified lens reported as explicitly classified")
	}
	if len(content) == 0 {
		t.Fatalf("the default clearing rule is empty, which would be a permanent escalation")
	}
	if _, explicit := provenance.ClearingRule("test-capitulation"); !explicit {
		t.Fatalf("a classified lens reported as defaulted, so the two answers are not distinguishable")
	}
}

// TestARefusedRunDoesNotEstablishALanesHistory is U5. Run 1 under a fresh
// self-asserted lane escalates and, because the escalated tier cannot reach a
// reviewer, records an outcome of "error". That record is bookkeeping about a
// run that never reached a verdict, and treating it as this lane's history is
// what made one throwaway invocation the price of a clean slate.
func TestARefusedRunDoesNotEstablishALanesHistory(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		outcome string
		want    bool
	}{
		{"pass", true},
		{"fail", true},
		{"error", false},
		{"", false},
		{"something-new", false},
	} {
		t.Run(probe.outcome, func(t *testing.T) {
			t.Parallel()
			got := provenance.HasVerdictHistory([]provenance.Record{
				record("c1", func(r *provenance.Record) { r.Outcome = probe.outcome }),
			})
			if got != probe.want {
				t.Fatalf("HasVerdictHistory(outcome %q) = %v, want %v", probe.outcome, got, probe.want)
			}
		})
	}
}
