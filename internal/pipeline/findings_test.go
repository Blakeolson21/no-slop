package pipeline

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestMergeFindingsJSON_UsesPipelineLineageAcrossRewording(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","id_generated":true,"continuity_token":"token-1","severity":"warning","description":"first"}],"summary":"1 finding"}`
	additionalRaw := `{"findings":[{"id":"review-1","id_generated":true,"continuity_token":"token-1","severity":"error","description":"second"}],"summary":"1 finding"}`

	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("expected one lineage, got %d", len(merged.Items))
	}
	if merged.Items[0].Description != "first" {
		t.Fatalf("unexpected merged findings: %#v", merged.Items)
	}
}

func TestMergeFindingsJSON_DistinctPipelineLineagesDoNotCollapse(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_generated":true,"continuity_token":"token-a","severity":"warning","file":"auth.go","line":12,"description":"authentication token fails"}]}`
	additionalRaw := `{"findings":[{"id":"review-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","id_generated":true,"continuity_token":"token-b","severity":"warning","file":"auth.go","line":12,"description":"authentication token fails"}]}`

	merged, err := types.ParseFindingsJSON(mergeFindingsJSON(existingRaw, additionalRaw))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("distinct pipeline lineages collapsed: %#v", merged.Items)
	}
	carried, err := types.ParseFindingsJSON(mergeCarriedFindingsJSON(additionalRaw, existingRaw, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if len(carried.Items) != 2 {
		t.Fatalf("distinct carried pipeline lineages collapsed: %#v", carried.Items)
	}
}

func TestMergeCarriedFindingsJSON_PreservesExplicitIDAcrossRephrasing(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_generated":true,"continuity_token":"token-a","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"}],"risk_level":"medium","risk_rationale":"Needs review."}`
	freshRaw := `{"findings":[{"id":"review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_generated":true,"continuity_token":"token-a","severity":"error","file":"manager.go","line":88,"description":"credentials are invalidated prematurely","action":"auto-fix"}],"risk_level":"high","risk_rationale":"Reproduced."}`

	mergedRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("findings = %#v, want one stable defect", merged.Items)
	}
	if merged.Items[0].ID != "review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || merged.Items[0].Description != "credentials are invalidated prematurely" || merged.Items[0].Action != "ask-user" {
		t.Fatalf("merged finding = %#v", merged.Items[0])
	}
}

func TestMergeCarriedFindingsJSON_MatchedLineagePreservesEffectiveRisk(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_generated":true,"continuity_token":"token-a","severity":"error","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user","review_scope":"source"}],"testing_summary":"Reproduced data loss.","risk_level":"high","risk_rationale":"Data can be lost.","risk_scope":"source-or-external"}`
	freshRaw := `{"findings":[{"id":"review-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id_generated":true,"continuity_token":"token-a","severity":"warning","file":"manager.go","line":88,"description":"loader still races","action":"auto-fix","review_scope":"source"}],"testing_summary":"Narrow retest passed.","risk_level":"low","risk_rationale":"Narrow path is safe.","risk_scope":"source-or-external"}`

	merged, err := types.ParseFindingsJSON(mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("findings = %#v, want one continued lineage", merged.Items)
	}
	if merged.Items[0].Action != "ask-user" || merged.RiskLevel != "high" {
		t.Fatalf("effective finding = %#v, risk = %q", merged.Items[0], merged.RiskLevel)
	}
	if !strings.Contains(merged.TestingSummary, "Reproduced data loss") || !strings.Contains(merged.TestingSummary, "Narrow retest passed") {
		t.Fatalf("testing summary = %q", merged.TestingSummary)
	}
}

func TestMergeReappearedFindingsJSONPreservesSelectedLineageSemanticsOnly(t *testing.T) {
	priorRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"error","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user","review_scope":"source"},{"id":"review-b","id_generated":true,"continuity_token":"token-b","severity":"warning","description":"narrow omitted issue","action":"ask-user","review_scope":"source"}],"tested":["reproduced data loss"],"testing_summary":"Full reproduction failed.","risk_level":"high","risk_rationale":"Data can be lost.","risk_scope":"source-or-external"}`
	freshRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"info","file":"loader.go","line":12,"description":"unsafe loader","action":"no-op","review_scope":"source"}],"tested":["narrow retest"],"testing_summary":"Narrow path passed.","risk_level":"low","risk_rationale":"Narrow path is safe.","risk_scope":"source-or-external"}`

	merged, err := types.ParseFindingsJSON(mergeReappearedFindingsJSON(freshRaw, priorRaw))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 || merged.Items[0].ID != "review-a" {
		t.Fatalf("reappeared findings = %#v, want only selected lineage A", merged.Items)
	}
	if merged.Items[0].Action != types.ActionAskUser || merged.Items[0].Severity != "error" || merged.RiskLevel != "high" {
		t.Fatalf("reappeared semantics = %#v, risk %q", merged.Items[0], merged.RiskLevel)
	}
	if !strings.Contains(merged.TestingSummary, "Full reproduction failed") || !strings.Contains(merged.TestingSummary, "Narrow path passed") || len(merged.Tested) != 2 {
		t.Fatalf("reappeared evidence = tested %#v, summary %q", merged.Tested, merged.TestingSummary)
	}
}

func TestMergeReappearedFindingsJSONDropsClearedLineageAggregateEvidence(t *testing.T) {
	priorRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"warning","description":"surviving defect","action":"ask-user","review_scope":"source"},{"id":"review-b","id_generated":true,"continuity_token":"token-b","severity":"error","description":"cleared defect","action":"ask-user","review_scope":"source"}],"tested":["reproduced cleared defect"],"testing_summary":"Cleared defect corrupts data.","artifacts":[{"kind":"log","label":"cleared-defect.log"}],"risk_level":"high","risk_rationale":"Cleared defect can corrupt data.","risk_scope":"source-or-external"}`
	freshRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"info","description":"surviving defect","action":"no-op","review_scope":"source"}],"tested":["retested surviving defect"],"testing_summary":"Surviving defect remains bounded.","risk_level":"low","risk_rationale":"Current review is bounded.","risk_scope":"source-or-external"}`

	merged, err := types.ParseFindingsJSON(mergeReappearedFindingsJSON(freshRaw, priorRaw))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 || merged.Items[0].ID != "review-a" || merged.Items[0].Action != types.ActionAskUser || merged.Items[0].Severity != "warning" {
		t.Fatalf("surviving lineage semantics = %#v", merged.Items)
	}
	if len(merged.Tested) != 1 || merged.Tested[0] != "retested surviving defect" || merged.TestingSummary != "Surviving defect remains bounded." || len(merged.Artifacts) != 0 {
		t.Fatalf("cleared lineage evidence survived: tested=%#v summary=%q artifacts=%#v", merged.Tested, merged.TestingSummary, merged.Artifacts)
	}
	if merged.RiskLevel != "medium" || merged.RiskScope != types.FindingsRiskScopeSourceOrExternal || strings.Contains(merged.RiskRationale, "Cleared defect") {
		t.Fatalf("recomputed risk = %q/%q %q", merged.RiskLevel, merged.RiskScope, merged.RiskRationale)
	}
}

func TestMergeReappearedFindingsJSONCorroboratesUniqueGeneratedStructure(t *testing.T) {
	priorRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"error","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user","review_scope":"source"}],"risk_level":"high","risk_rationale":"Data can be lost.","risk_scope":"source-or-external"}`
	freshRaw := `{"findings":[{"id":"review-c","id_generated":true,"continuity_token":"token-c","severity":"info","file":"loader.go","line":12,"description":"unsafe loader","action":"no-op","review_scope":"source"}],"risk_level":"low","risk_rationale":"Narrow path is safe.","risk_scope":"source-or-external"}`

	merged, err := types.ParseFindingsJSON(mergeReappearedFindingsJSON(freshRaw, priorRaw))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 || merged.Items[0].ID != "review-a" || merged.Items[0].ContinuityToken != "token-a" {
		t.Fatalf("unique structural continuation = %#v", merged.Items)
	}
	if merged.Items[0].Action != types.ActionAskUser || merged.Items[0].Severity != "error" || merged.RiskLevel != "high" {
		t.Fatalf("continued semantics = %#v, risk %q", merged.Items[0], merged.RiskLevel)
	}
}

func TestMergeReappearedFindingsJSONPreservesAmbiguousGeneratedLineages(t *testing.T) {
	priorRaw := `{"findings":[{"id":"review-a","id_generated":true,"continuity_token":"token-a","severity":"error","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"},{"id":"review-b","id_generated":true,"continuity_token":"token-b","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-c","id_generated":true,"continuity_token":"token-c","severity":"info","file":"loader.go","line":12,"description":"unsafe loader","action":"no-op"}]}`

	merged, err := types.ParseFindingsJSON(mergeReappearedFindingsJSON(freshRaw, priorRaw))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 3 {
		t.Fatalf("ambiguous structural continuation dropped lineages: %#v", merged.Items)
	}
	ids := map[string]bool{}
	for _, item := range merged.Items {
		ids[item.ID] = true
	}
	if !ids["review-a"] || !ids["review-b"] || !ids["review-c"] {
		t.Fatalf("ambiguous structural identities = %#v", merged.Items)
	}
}

func TestReconcileReviewFindingsPreservesRejectedSelectedClaim(t *testing.T) {
	prior, err := types.NormalizeFindings(types.Findings{Items: []types.Finding{{
		Severity:    "error",
		File:        "loader.go",
		Line:        42,
		Description: "nil dereference remains reachable",
		Action:      types.ActionAskUser,
		ReviewScope: types.FindingReviewScopeSource,
	}}}, "review", nil)
	if err != nil {
		t.Fatal(err)
	}
	priorRaw, err := types.MarshalFindingsJSON(prior)
	if err != nil {
		t.Fatal(err)
	}
	fresh := types.Findings{Items: []types.Finding{{
		PriorID:              prior.Items[0].ID,
		PriorContinuityToken: prior.Items[0].ContinuityToken,
		Severity:             "warning",
		File:                 "loader.go",
		Line:                 42,
		Description:          "SQL injection remains reachable",
		Action:               types.ActionAutoFix,
		ReviewScope:          types.FindingReviewScopeSource,
	}}}

	reconciled, _, err := ReconcileReviewFindings(fresh, priorRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciled.Items) != 2 {
		t.Fatalf("ambiguous selected claim dropped a finding: %#v", reconciled.Items)
	}
	byDescription := make(map[string]types.Finding, len(reconciled.Items))
	for _, item := range reconciled.Items {
		byDescription[item.Description] = item
	}
	if byDescription["nil dereference remains reachable"].ID != prior.Items[0].ID || byDescription["nil dereference remains reachable"].Action != types.ActionAskUser {
		t.Fatalf("prior selected lineage changed: %#v", reconciled.Items)
	}
	if byDescription["SQL injection remains reachable"].ID == prior.Items[0].ID {
		t.Fatalf("unrelated finding inherited selected lineage: %#v", reconciled.Items)
	}
}

func TestFilterDeferredPipelineOwnedDeliveryRecomputesRetainedSourceRisk(t *testing.T) {
	filtered, dropped := FilterDeferredPipelineOwnedDeliveryFindings(types.Findings{
		Items: []types.Finding{
			{Severity: "error", Description: "source corruption remains", ReviewScope: types.FindingReviewScopeSource},
			{Severity: "error", Description: "PR publication is pending", ReviewScope: types.FindingReviewScopePipelineOwnedDelivery},
		},
		RiskLevel: "low",
		RiskScope: types.FindingsRiskScopePipelineOwnedDelivery,
	})
	if dropped != 1 || len(filtered.Items) != 1 {
		t.Fatalf("filtered findings = %#v, dropped = %d", filtered.Items, dropped)
	}
	if filtered.RiskLevel != "high" || filtered.RiskScope != types.FindingsRiskScopeSourceOrExternal {
		t.Fatalf("retained source risk = %q/%q", filtered.RiskLevel, filtered.RiskScope)
	}
}

func TestReconcileReviewFindingsRestoresUserLineageSemanticsBeforeFiltering(t *testing.T) {
	prior := types.Findings{Items: []types.Finding{{
		ID:               "user-1",
		IDGenerated:      true,
		ContinuityToken:  "00112233445566778899aabbccddeeff",
		Severity:         "error",
		File:             "loader.go",
		Description:      "operator-added defect",
		Action:           types.ActionAskUser,
		Source:           types.FindingSourceUser,
		UserInstructions: "preserve the compatibility path",
		ReviewScope:      types.FindingReviewScopeSource,
	}}}
	priorRaw, err := types.MarshalFindingsJSON(prior)
	if err != nil {
		t.Fatal(err)
	}
	fresh := types.Findings{Items: []types.Finding{{
		PriorID:              "user-1",
		PriorContinuityToken: "00112233445566778899aabbccddeeff",
		Severity:             "warning",
		File:                 "loader.go",
		Description:          "operator-added defect",
		Action:               types.ActionNoOp,
		ReviewScope:          types.FindingReviewScopePipelineOwnedDelivery,
	}}}

	reconciled, dropped, err := ReconcileReviewFindings(fresh, priorRaw)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 || len(reconciled.Items) != 1 {
		t.Fatalf("reconciled findings = %#v, dropped = %d", reconciled.Items, dropped)
	}
	item := reconciled.Items[0]
	if item.ID != "user-1" || item.ContinuityToken != prior.Items[0].ContinuityToken || item.Source != types.FindingSourceUser {
		t.Fatalf("user lineage changed: %#v", item)
	}
	if item.Action != types.ActionAskUser || item.Severity != "error" || item.UserInstructions != prior.Items[0].UserInstructions || item.ReviewScope != types.FindingReviewScopeSource {
		t.Fatalf("user lineage semantics changed: %#v", item)
	}
}

func TestMergeCarriedFindingsJSON_ExcludesPipelineDeliveryFromEffectiveRisk(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-delivery","severity":"error","description":"PR not pushed","action":"ask-user","review_scope":"pipeline-owned-delivery"}],"risk_level":"high","risk_rationale":"PR is absent.","risk_scope":"pipeline-owned-delivery"}`
	freshRaw := `{"findings":[{"id":"review-source","severity":"info","description":"bounded source concern","action":"ask-user","review_scope":"source"}],"risk_level":"low","risk_rationale":"Source change is bounded.","risk_scope":"source-or-external"}`

	merged, err := types.ParseFindingsJSON(mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if merged.RiskLevel != "low" || merged.RiskScope != types.FindingsRiskScopeSourceOrExternal {
		t.Fatalf("effective source risk = %q/%q, want low/source-or-external", merged.RiskLevel, merged.RiskScope)
	}
	if !strings.Contains(merged.RiskRationale, "excluded from source/external risk") {
		t.Fatalf("risk rationale = %q", merged.RiskRationale)
	}
}

func TestMergeCarriedFindingsJSON_DoesNotTrustUncorroboratedExplicitID(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-1","severity":"warning","file":"loader.go","line":12,"description":"unsafe loader","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-1","severity":"error","file":"loader.go","line":12,"description":"cache write can deadlock","action":"auto-fix"}]}`

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

func TestMergeCarriedFindingsJSON_PreservesPriorWhenClaimIsUnrelated(t *testing.T) {
	prior, err := types.NormalizeFindings(types.Findings{Items: []types.Finding{{
		File:        "loader.go",
		Line:        42,
		Description: "unsafe loader",
		Action:      types.ActionAskUser,
	}}}, "review", nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := types.NormalizeFindings(types.Findings{Items: []types.Finding{{
		PriorID:              prior.Items[0].ID,
		PriorContinuityToken: prior.Items[0].ContinuityToken,
		File:                 "loader.go",
		Line:                 42,
		Description:          "authentication token leaks in logs",
		Action:               types.ActionAutoFix,
	}}}, "review", prior.Items)
	if err != nil {
		t.Fatal(err)
	}
	priorRaw, err := types.MarshalFindingsJSON(prior)
	if err != nil {
		t.Fatal(err)
	}
	freshRaw, err := types.MarshalFindingsJSON(fresh)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := types.ParseFindingsJSON(mergeCarriedFindingsJSON(freshRaw, priorRaw, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("unrelated claim replaced carried finding: %#v", merged.Items)
	}
	byDescription := make(map[string]types.Finding, len(merged.Items))
	for _, item := range merged.Items {
		byDescription[item.Description] = item
	}
	if byDescription["unsafe loader"].ID != prior.Items[0].ID || byDescription["unsafe loader"].Action != types.ActionAskUser {
		t.Fatalf("prior finding changed: %#v", merged.Items)
	}
	if byDescription["authentication token leaks in logs"].ID == prior.Items[0].ID {
		t.Fatalf("new finding inherited prior lineage: %#v", merged.Items)
	}
}

func TestMergeCarriedFindingsJSON_DoesNotTrustReviewerIDCollision(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first defect","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-1","severity":"error","description":"second defect","action":"auto-fix"}]}`

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

func TestMergeCarriedFindingsJSON_PreservesAmbiguousLegacyLineages(t *testing.T) {
	carriedRaw := `{"findings":[{"id":"review-old-a","severity":"error","file":"loader.go","line":42,"description":"unsafe loader","action":"ask-user"},{"id":"review-old-b","severity":"error","file":"loader.go","line":42,"description":"unsafe loader","action":"ask-user"}]}`
	freshRaw := `{"findings":[{"id":"review-fresh","severity":"error","file":"loader.go","line":42,"description":"unsafe loader","action":"auto-fix"}]}`

	merged, err := types.ParseFindingsJSON(mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 3 {
		t.Fatalf("ambiguous legacy lineages collapsed: %#v", merged.Items)
	}
	ids := make(map[string]bool, len(merged.Items))
	for _, item := range merged.Items {
		if ids[item.ID] {
			t.Fatalf("ambiguous lineages share ID %q: %#v", item.ID, merged.Items)
		}
		ids[item.ID] = true
	}
	if !ids["review-old-a"] || !ids["review-old-b"] {
		t.Fatalf("carried identities changed: %#v", merged.Items)
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

func TestReviewEvidenceFollowsSurvivingLineagesAcrossSelection(t *testing.T) {
	prior := types.Findings{
		Items: []types.Finding{
			{
				ID:              "review-a",
				IDGenerated:     true,
				ContinuityToken: "token-a",
				Severity:        "error",
				File:            "a.go",
				Line:            10,
				Description:     "defect A",
				Action:          types.ActionAutoFix,
				ReviewScope:     types.FindingReviewScopeSource,
				Evidence: &types.FindingEvidence{
					Tested:         []string{"reproduce A"},
					TestingSummary: "A remains reproducible.",
					Artifacts:      []types.TestArtifact{{Kind: "log", Label: "A trace", Content: "A"}},
				},
			},
			{
				ID:              "review-b",
				IDGenerated:     true,
				ContinuityToken: "token-b",
				Severity:        "error",
				File:            "b.go",
				Line:            20,
				Description:     "defect B",
				Action:          types.ActionAutoFix,
				ReviewScope:     types.FindingReviewScopeSource,
				Evidence: &types.FindingEvidence{
					Tested:         []string{"reproduce B"},
					TestingSummary: "B remains reproducible.",
					Artifacts:      []types.TestArtifact{{Kind: "log", Label: "B trace", Content: "B"}},
				},
			},
		},
		Tested:         []string{"reproduce A", "reproduce B"},
		TestingSummary: "A remains reproducible.\n\nB remains reproducible.",
		Artifacts:      []types.TestArtifact{{Kind: "log", Label: "A trace", Content: "A"}, {Kind: "log", Label: "B trace", Content: "B"}},
	}
	priorRaw, err := types.MarshalFindingsJSON(prior)
	if err != nil {
		t.Fatal(err)
	}
	fresh := types.Findings{Items: []types.Finding{{
		PriorID:              "review-b",
		PriorContinuityToken: "token-b",
		Severity:             "error",
		File:                 "b.go",
		Line:                 20,
		Description:          "defect B",
		Action:               types.ActionAutoFix,
		ReviewScope:          types.FindingReviewScopeSource,
		Evidence:             &types.FindingEvidence{},
	}}}
	freshRaw, err := types.MarshalFindingsJSON(fresh)
	if err != nil {
		t.Fatal(err)
	}
	freshRaw, err = normalizeFindingsJSON(freshRaw, "review", priorRaw)
	if err != nil {
		t.Fatal(err)
	}
	freshRaw = mergeReappearedFindingsJSON(freshRaw, priorRaw)

	for _, tc := range []struct {
		name     string
		selected []string
	}{
		{name: "selected subset", selected: []string{"review-a"}},
		{name: "selected all", selected: []string{"review-a", "review-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			carriedRaw := excludeFindingsJSON(priorRaw, tc.selected)
			effectiveRaw := mergeCarriedFindingsJSON(freshRaw, carriedRaw, "review")
			effective, err := types.ParseFindingsJSON(effectiveRaw)
			if err != nil {
				t.Fatal(err)
			}
			if len(effective.Items) != 1 || effective.Items[0].ID != "review-b" {
				t.Fatalf("surviving findings = %#v", effective.Items)
			}
			if len(effective.Tested) != 1 || effective.Tested[0] != "reproduce B" || effective.TestingSummary != "B remains reproducible." {
				t.Fatalf("surviving tested evidence = %#v, %q", effective.Tested, effective.TestingSummary)
			}
			if len(effective.Artifacts) != 1 || effective.Artifacts[0].Label != "B trace" {
				t.Fatalf("surviving artifacts = %#v", effective.Artifacts)
			}
		})
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
