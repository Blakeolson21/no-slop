package pipeline

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestMergeFindingsJSON_KeepsDistinctFindingsWithSameAutoID(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","id_generated":true,"severity":"warning","description":"first"}],"summary":"1 finding"}`
	additionalRaw := `{"findings":[{"id":"review-1","id_generated":true,"severity":"error","description":"second"}],"summary":"1 finding"}`

	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(merged.Items))
	}
	if merged.Items[0].Description != "first" || merged.Items[1].Description != "second" {
		t.Fatalf("unexpected merged findings: %#v", merged.Items)
	}
}

func TestMergeCarriedFindingsJSON_PreservesExplicitIDAcrossRephrasing(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"loader-race","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"}],"risk_level":"medium","risk_rationale":"Needs review."}`
	freshRaw := `{"findings":[{"id":"loader-race","severity":"error","file":"loader.go","line":12,"description":"loader races concurrent shutdown","action":"auto-fix"}],"risk_level":"high","risk_rationale":"Reproduced."}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("findings = %#v, want one stable defect", merged.Items)
	}
	if merged.Items[0].ID != "loader-race" || merged.Items[0].Description != "loader races concurrent shutdown" || merged.Items[0].Action != "ask-user" {
		t.Fatalf("merged finding = %#v", merged.Items[0])
	}
}

func TestMergeCarriedFindingsJSON_DoesNotTrustUncorroboratedExplicitID(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-1","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-1","severity":"error","file":"cache.go","line":30,"description":"cache write can deadlock","action":"auto-fix"}]}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("findings = %#v, want both unrelated defects", merged.Items)
	}
	byDescription := make(map[string]types.Finding, len(merged.Items))
	for _, item := range merged.Items {
		byDescription[item.Description] = item
	}
	if byDescription["unsafe loader"].Action != "ask-user" || byDescription["cache write can deadlock"].Action != "auto-fix" {
		t.Fatalf("explicit ID collision changed findings: %#v", merged.Items)
	}
	if merged.Items[0].ID == merged.Items[1].ID {
		t.Fatalf("explicit ID collision survived merge: %#v", merged.Items)
	}
}

func TestMergeCarriedFindingsJSON_DoesNotTrustGeneratedIDCollision(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-1","id_generated":true,"severity":"warning","description":"first defect","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-1","id_generated":true,"severity":"error","description":"second defect","action":"auto-fix"}]}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("findings = %#v, want two distinct defects", merged.Items)
	}
	if merged.Items[0].ID == merged.Items[1].ID {
		t.Fatalf("generated ID collision survived merge: %#v", merged.Items)
	}
}

func TestRetainMatchingFindingsJSON_DropsFindingsMissingFromLatestReview(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first"},{"id":"review-2","severity":"error","description":"second"}],"summary":"2 findings"}`
	keepRaw := `{"findings":[{"id":"review-7","severity":"error","description":"second"},{"id":"review-8","severity":"warning","description":"third"}],"summary":"2 findings"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].Description != "second" {
		t.Fatalf("unexpected retained findings: %#v", retained.Items)
	}
}

func TestRetainMatchingFindingsJSON_MatchesFindingsAfterLineShift(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`
	keepRaw := `{"findings":[{"id":"review-9","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"1 finding"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected retained finding: %#v", retained.Items)
	}
}

func TestRetainMatchingFindingsJSON_DoesNotKeepDistinctDuplicateLines(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"},{"id":"dismissed-2","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"2 findings"}`
	keepRaw := `{"findings":[{"id":"review-9","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected retained findings: %#v", retained.Items)
	}
}

func TestAutoFixableFindingsJSON_FiltersToAutoFix(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"error","description":"bug","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"design choice","action":"ask-user"},{"id":"review-3","severity":"warning","description":"missing check","action":"auto-fix"},{"id":"review-4","severity":"info","description":"note","action":"no-op"}],"risk_level":"medium","risk_rationale":"Mixed."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	fixable, err := types.ParseFindingsJSON(fixableRaw)
	if err != nil {
		t.Fatalf("parse auto-fixable findings: %v", err)
	}
	if len(fixable.Items) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(fixable.Items))
	}
	if fixable.Items[0].ID != "review-1" || fixable.Items[1].ID != "review-3" {
		t.Fatalf("unexpected findings: %#v", fixable.Items)
	}
}

func TestAutoFixableFindingsJSON_AllAskUser(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"warning","description":"choice","action":"ask-user"}],"risk_level":"high","risk_rationale":"Needs review."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	if fixableRaw != "" {
		t.Fatalf("expected empty string for all-ask-user findings, got %q", fixableRaw)
	}
}

func TestAutoFixableFindingsJSON_EmptyInput(t *testing.T) {
	if got := autoFixableFindingsJSON(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
}

func TestAutoFixableFindingsJSON_AllNoOp(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"info","description":"note","action":"no-op"}],"risk_level":"low","risk_rationale":"Clean."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	if fixableRaw != "" {
		t.Fatalf("expected empty string for all-no-op findings, got %q", fixableRaw)
	}
}

func TestMergeFindingsJSON_DeduplicatesShiftedUniqueDismissedFinding(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`
	additionalRaw := `{"findings":[{"id":"dismissed-2","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"1 finding"}`

	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(merged.Items))
	}
	if merged.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected merged findings: %#v", merged.Items)
	}
}

func TestMergeCarriedFindingsJSON_PreservesIdentityAcrossReclassification(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-1","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user","review_scope":"source","category":"documentation"}],"risk_level":"medium","risk_rationale":"Needs review."}`
	freshRaw := `{"findings":[{"id":"review-9","severity":"error","file":"loader.go","line":12,"description":"unsafe loader","action":"no-op","review_scope":"external-delivery","category":"lint"}],"risk_level":"high","risk_rationale":"Reclassified."}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("findings = %#v, want one stable defect", merged.Items)
	}
	if merged.Items[0].ID != "review-1" || merged.Items[0].Severity != "error" || merged.Items[0].Action != "ask-user" {
		t.Fatalf("merged finding = %#v", merged.Items[0])
	}
}

func TestMergeCarriedFindingsJSON_RecomputesEffectiveRiskAndPreservesEvidence(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-2","severity":"error","description":"remaining concern","action":"ask-user","review_scope":"source"}],"testing_summary":"Reproduced the remaining race under load.","risk_level":"high","risk_rationale":"Selected finding can corrupt data.","risk_scope":"source-or-external"}`
	freshRaw := `{"findings":[],"testing_summary":"Verified the selected defect is fixed.","risk_level":"low","risk_rationale":"The selected defect is fixed.","risk_scope":"source-or-external"}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if merged.RiskLevel != "high" || strings.Contains(merged.RiskRationale, "Selected finding") || strings.Contains(merged.RiskRationale, "selected defect") {
		t.Fatalf("aggregate risk = %q %q", merged.RiskLevel, merged.RiskRationale)
	}
	if !strings.Contains(merged.TestingSummary, "Reproduced the remaining race") || !strings.Contains(merged.TestingSummary, "Verified the selected defect") {
		t.Fatalf("testing summary = %q", merged.TestingSummary)
	}
}

func TestExcludeFindingsJSON_DropsAggregateRiskForSubset(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"error","description":"selected defect"},{"id":"review-2","severity":"warning","description":"remaining concern"}],"risk_level":"high","risk_rationale":"Selected defect can corrupt data.","risk_scope":"source-or-external"}`

	excludedRaw := excludeFindingsJSON(raw, []string{"review-1"})
	excluded, err := types.ParseFindingsJSON(excludedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Items) != 1 || excluded.Items[0].ID != "review-2" {
		t.Fatalf("remaining findings = %#v", excluded.Items)
	}
	if excluded.RiskLevel != "" || excluded.RiskRationale != "" || excluded.RiskScope != "" {
		t.Fatalf("subset retained aggregate risk: %#v", excluded)
	}
}

func TestFilterFindingsJSON_EmptySelectionReturnsEmptyFindings(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"error","description":"first"}],"summary":"1 finding"}`

	filteredRaw := filterFindingsJSON(raw, nil)
	filtered, err := types.ParseFindingsJSON(filteredRaw)
	if err != nil {
		t.Fatalf("parse filtered findings: %v", err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(filtered.Items))
	}
	if filtered.Summary != "0 selected findings" {
		t.Fatalf("summary = %q, want %q", filtered.Summary, "0 selected findings")
	}
}
