package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// findingIDsJSON extracts the finding IDs from a findings JSON payload and
// returns them as a JSON array string. Empty result means there were no
// findings or parsing failed.
func findingIDsJSON(raw string) string {
	return marshalFindingIDs(findingIDList(raw))
}

func findingIDList(raw string) []string {
	if raw == "" {
		return nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(findings.Items))
	for _, item := range findings.Items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// marshalFindingIDs encodes a list of finding IDs as a JSON array. Empty
// input returns an empty string so the caller can leave the DB column NULL.
func marshalFindingIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func findingKey(item types.Finding) types.FindingIdentity {
	return item.Identity()
}

func findingFingerprint(item types.Finding) types.FindingIdentity {
	return item.Fingerprint()
}

func hasFindingMatch(item types.Finding, stableIDs map[string][]types.Finding, itemOccurrenceCounts, candidateOccurrenceCounts map[string]int, itemIdentityCounts, candidateIdentityCounts, itemFingerprintCounts, candidateFingerprintCounts map[types.FindingIdentity]int) bool {
	return types.FindingMatches(item, stableIDs, itemOccurrenceCounts, candidateOccurrenceCounts, itemIdentityCounts, candidateIdentityCounts, itemFingerprintCounts, candidateFingerprintCounts)
}

func normalizeFindingsJSON(raw string, prefix string, existingRaw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw, nil
	}
	if prefix == string(types.StepReview) && len(findings.Items) == 1 && findings.Items[0].Evidence == nil &&
		(len(findings.Tested) > 0 || findings.TestingSummary != "" || len(findings.Artifacts) > 0) {
		findings.Items[0].Evidence = &types.FindingEvidence{
			Tested:         append([]string(nil), findings.Tested...),
			TestingSummary: findings.TestingSummary,
			Artifacts:      append([]types.TestArtifact(nil), findings.Artifacts...),
		}
	}
	var existing []types.Finding
	if existingRaw != "" {
		parsed, parseErr := types.ParseFindingsJSON(existingRaw)
		if parseErr != nil {
			return "", parseErr
		}
		existing = parsed.Items
	}
	normalized, err := types.NormalizeFindings(findings, prefix, existing)
	if err != nil {
		return "", err
	}
	normalizedRaw, err := types.MarshalFindingsJSON(normalized)
	if err != nil {
		return raw, nil
	}
	return normalizedRaw, nil
}

func excludeFindingsJSON(raw string, ids []string) string {
	if raw == "" {
		return ""
	}
	if len(ids) == 0 {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return ""
	}
	excluded := types.ExcludeFindings(findings, ids)
	if len(excluded.Items) == 0 {
		return ""
	}
	if len(excluded.Items) != len(findings.Items) {
		if !rebuildAttributedEvidence(&excluded) {
			excluded.Tested = nil
			excluded.TestingSummary = ""
			excluded.Artifacts = nil
		}
		excluded.RiskLevel = ""
		excluded.RiskRationale = ""
		excluded.RiskScope = ""
	}
	excludedRaw, err := types.MarshalFindingsJSON(excluded)
	if err != nil {
		return ""
	}
	return excludedRaw
}

func prepareReviewSelectionTruth(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return "", err
	}
	findings, err = types.EnsureFindingOccurrenceTokens(findings)
	if err != nil {
		return "", err
	}
	if findings.SharedEvidence == nil {
		findings.SharedEvidence = residualSharedEvidence(findings)
	}
	rebuildAttributedEvidence(&findings)
	return types.MarshalFindingsJSON(findings)
}

// mergeCarriedFindingsJSON forms the effective gate truth for a scope-limited
// round. Already-shown findings keep their stable IDs and cannot have their
// action relaxed by a later restatement. New-ID collisions are reassigned
// before publication.
func mergeCarriedFindingsJSON(freshRaw, carriedRaw, prefix string) string {
	if carriedRaw == "" {
		return freshRaw
	}
	if freshRaw == "" {
		return carriedRaw
	}
	fresh, err := types.ParseFindingsJSON(freshRaw)
	if err != nil {
		return carriedRaw
	}
	carried, err := types.ParseFindingsJSON(carriedRaw)
	if err != nil {
		return freshRaw
	}
	merged := fresh
	merged.SharedEvidence = mergeFindingEvidence(sharedEvidenceOwner(fresh), sharedEvidenceOwner(carried))
	freshCounts := types.CountFindingFingerprints(fresh.Items)
	carriedCounts := types.CountFindingFingerprints(carried.Items)
	freshIdentityCounts := countFindingIdentities(fresh.Items)
	carriedIdentityCounts := countFindingIdentities(carried.Items)
	freshOccurrenceCounts := types.CountFindingOccurrences(fresh.Items)
	carriedOccurrenceCounts := types.CountFindingOccurrences(carried.Items)
	carriedIdentity := make(map[int]bool, len(carried.Items))
	carriedCount := 0
	for _, old := range carried.Items {
		match := -1
		for i, current := range merged.Items {
			identity := findingKey(current)
			legacyMatch := (!current.HasLineage() || !old.HasLineage()) && ((identity == findingKey(old) && freshIdentityCounts[identity] == 1 && carriedIdentityCounts[identity] == 1) ||
				(findingFingerprint(current) == findingFingerprint(old) && freshCounts[findingFingerprint(current)] == 1 && carriedCounts[findingFingerprint(old)] == 1))
			occurrenceMatch := types.FindingOccurrenceCorroborates(current, old) && freshOccurrenceCounts[current.OccurrenceToken] == 1 && carriedOccurrenceCounts[old.OccurrenceToken] == 1
			if occurrenceMatch || types.FindingIDCorroborates(current, old) || legacyMatch {
				match = i
				break
			}
		}
		if match >= 0 {
			merged.Items[match].Evidence = mergeFindingEvidence(merged.Items[match].Evidence, old.Evidence)
			merged.Items[match].ID = old.ID
			merged.Items[match].IDGenerated = old.IDGenerated
			merged.Items[match].ContinuityToken = old.ContinuityToken
			merged.Items[match].Action = stricterFindingAction(old.Action, merged.Items[match].Action)
			carriedIdentity[match] = true
			carriedCount++
			continue
		}
		merged.Items = append(merged.Items, old)
		carriedIdentity[len(merged.Items)-1] = true
		carriedCount++
	}

	reserved := make(map[string]bool, len(merged.Items))
	for i, item := range merged.Items {
		if carriedIdentity[i] && item.ID != "" {
			reserved[item.ID] = true
		}
	}
	nextID := 1
	for i := range merged.Items {
		id := merged.Items[i].ID
		if carriedIdentity[i] {
			continue
		}
		if id != "" && !reserved[id] {
			reserved[id] = true
			continue
		}
		for {
			candidate := fmt.Sprintf("%s-%d", prefix, nextID)
			nextID++
			if !reserved[candidate] {
				merged.Items[i].ID = candidate
				merged.Items[i].IDGenerated = true
				reserved[candidate] = true
				break
			}
		}
	}

	merged.Summary = fmt.Sprintf("%d outstanding %s", len(merged.Items), pluralize(len(merged.Items), "finding", "findings"))
	if !rebuildAttributedEvidence(&merged) {
		merged.Tested = mergeComparable(fresh.Tested, carried.Tested)
		merged.Artifacts = mergeComparable(fresh.Artifacts, carried.Artifacts)
		merged.TestingSummary = mergeEvidenceSummary(fresh.TestingSummary, carried.TestingSummary)
	}
	if carriedCount > 0 {
		merged.RiskLevel, merged.RiskRationale, merged.RiskScope = effectiveFindingsRisk(merged.Items, fresh, carried, carriedCount)
	}
	encoded, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return carriedRaw
	}
	return encoded
}

func mergeReappearedFindingsJSON(freshRaw, priorRaw string) string {
	if freshRaw == "" || priorRaw == "" {
		return freshRaw
	}
	fresh, err := types.ParseFindingsJSON(freshRaw)
	if err != nil {
		return freshRaw
	}
	prior, err := types.ParseFindingsJSON(priorRaw)
	if err != nil {
		return freshRaw
	}
	freshCounts := types.CountFindingFingerprints(fresh.Items)
	priorCounts := types.CountFindingFingerprints(prior.Items)
	freshIdentityCounts := countFindingIdentities(fresh.Items)
	priorIdentityCounts := countFindingIdentities(prior.Items)
	freshOccurrenceCounts := types.CountFindingOccurrences(fresh.Items)
	matched := 0
	ambiguousPrior := make([]bool, len(prior.Items))
	matchedPrior := make([]bool, len(prior.Items))
	for i := range fresh.Items {
		current := &fresh.Items[i]
		lineageMatches := make([]int, 0, 1)
		occurrenceMatches := make([]int, 0, 1)
		structuralMatches := make([]int, 0, 1)
		for j := range prior.Items {
			old := prior.Items[j]
			if current.PriorID != "" && current.PriorContinuityToken != "" && old.HasLineage() &&
				current.PriorID == old.ID && current.PriorContinuityToken == old.ContinuityToken {
				ambiguousPrior[j] = true
			}
			if types.FindingIDCorroborates(*current, old) {
				lineageMatches = append(lineageMatches, j)
				continue
			}
			if types.FindingOccurrenceCorroborates(*current, old) {
				occurrenceMatches = append(occurrenceMatches, j)
				continue
			}
			if findingKey(*current) == findingKey(old) || findingFingerprint(*current) == findingFingerprint(old) {
				structuralMatches = append(structuralMatches, j)
			}
		}
		match := -1
		switch {
		case len(occurrenceMatches) == 1 && freshOccurrenceCounts[current.OccurrenceToken] == 1:
			match = occurrenceMatches[0]
		case len(occurrenceMatches) > 1:
			for _, j := range occurrenceMatches {
				ambiguousPrior[j] = true
			}
		case len(lineageMatches) == 1:
			match = lineageMatches[0]
		case len(lineageMatches) == 0:
			identity := findingKey(*current)
			fingerprint := findingFingerprint(*current)
			if len(structuralMatches) == 1 && ((identity == findingKey(prior.Items[structuralMatches[0]]) && freshIdentityCounts[identity] == 1 && priorIdentityCounts[identity] == 1) ||
				(fingerprint == findingFingerprint(prior.Items[structuralMatches[0]]) && freshCounts[fingerprint] == 1 && priorCounts[fingerprint] == 1)) {
				match = structuralMatches[0]
			} else {
				for _, j := range structuralMatches {
					ambiguousPrior[j] = true
				}
			}
		default:
			for _, j := range lineageMatches {
				ambiguousPrior[j] = true
			}
		}
		if match < 0 {
			continue
		}
		old := prior.Items[match]
		matchedPrior[match] = true
		current.Evidence = mergeFindingEvidence(current.Evidence, old.Evidence)
		current.ID = old.ID
		current.IDGenerated = old.IDGenerated
		current.ContinuityToken = old.ContinuityToken
		current.Action = stricterFindingAction(old.Action, current.Action)
		if severityRank(old.Severity) > severityRank(current.Severity) {
			current.Severity = old.Severity
		}
		if current.UserInstructions == "" {
			current.UserInstructions = old.UserInstructions
		}
		if current.ReviewScope == "" || (current.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery && old.ReviewScope != "") {
			current.ReviewScope = old.ReviewScope
		}
		if current.Category == "" {
			current.Category = old.Category
		}
		if old.Source == types.FindingSourceUser {
			current.Source = old.Source
		}
		matched++
	}
	cleanedClaims := false
	for i := range fresh.Items {
		if fresh.Items[i].PriorID != "" || fresh.Items[i].PriorContinuityToken != "" {
			cleanedClaims = true
			fresh.Items[i].PriorID = ""
			fresh.Items[i].PriorContinuityToken = ""
		}
	}
	for j, ambiguous := range ambiguousPrior {
		if ambiguous && !matchedPrior[j] {
			fresh.Items = append(fresh.Items, prior.Items[j])
			matchedPrior[j] = true
			matched++
		}
	}
	if matched == 0 {
		if !cleanedClaims {
			return freshRaw
		}
		encoded, err := types.MarshalFindingsJSON(fresh)
		if err != nil {
			return freshRaw
		}
		return encoded
	}
	fresh.SharedEvidence = mergeFindingEvidence(sharedEvidenceOwner(fresh), sharedEvidenceOwner(prior))
	fresh.Summary = fmt.Sprintf("%d outstanding %s", len(fresh.Items), pluralize(len(fresh.Items), "finding", "findings"))
	allPriorSurvived := true
	for _, survived := range matchedPrior {
		if !survived {
			allPriorSurvived = false
			break
		}
	}
	attributedEvidence := rebuildAttributedEvidence(&fresh)
	if allPriorSurvived {
		if !attributedEvidence {
			fresh.Tested = mergeComparable(fresh.Tested, prior.Tested)
			fresh.Artifacts = mergeComparable(fresh.Artifacts, prior.Artifacts)
			fresh.TestingSummary = mergeEvidenceSummary(fresh.TestingSummary, prior.TestingSummary)
		}
		fresh.RiskLevel, fresh.RiskRationale, fresh.RiskScope = effectiveFindingsRisk(fresh.Items, fresh, prior, matched)
	} else {
		fresh.RiskLevel, fresh.RiskRationale, fresh.RiskScope = survivingFindingsRisk(fresh)
	}
	encoded, err := types.MarshalFindingsJSON(fresh)
	if err != nil {
		return freshRaw
	}
	return encoded
}

func survivingFindingsRisk(findings types.Findings) (string, string, string) {
	rank := 0
	if findings.RiskScope != types.FindingsRiskScopePipelineOwnedDelivery {
		rank = riskRank(findings.RiskLevel)
	}
	retained := 0
	for _, item := range findings.Items {
		if item.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery {
			continue
		}
		retained++
		if itemRank := severityRank(item.Severity); itemRank > rank {
			rank = itemRank
		}
	}
	if rank == 0 {
		rank = riskRank("low")
	}
	rationale := fmt.Sprintf("Review risk recomputed from %d surviving %s.", retained, pluralize(retained, "finding", "findings"))
	return riskLevel(rank), rationale, types.FindingsRiskScopeSourceOrExternal
}

func ReconcileReviewFindings(findings types.Findings, priorRaw string) (types.Findings, int, error) {
	raw, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return types.Findings{}, 0, err
	}
	normalized, err := normalizeFindingsJSON(raw, string(types.StepReview), priorRaw)
	if err != nil {
		return types.Findings{}, 0, err
	}
	merged := mergeReappearedFindingsJSON(normalized, priorRaw)
	reconciled, err := types.ParseFindingsJSON(merged)
	if err != nil {
		return types.Findings{}, 0, err
	}
	filtered, dropped := FilterDeferredPipelineOwnedDeliveryFindings(reconciled)
	return filtered, dropped, nil
}

func FilterDeferredPipelineOwnedDeliveryFindings(findings types.Findings) (types.Findings, int) {
	if len(findings.Items) == 0 {
		return findings, 0
	}
	kept := make([]types.Finding, 0, len(findings.Items))
	dropped := 0
	for _, item := range findings.Items {
		if item.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery {
			dropped++
			continue
		}
		kept = append(kept, item)
	}
	if dropped == 0 {
		return findings, 0
	}
	out := findings
	out.Items = kept
	rebuildAttributedEvidence(&out)
	switch len(kept) {
	case 0:
		out.Summary = "no review findings remain"
	case 1:
		out.Summary = "1 review finding remains"
	default:
		out.Summary = fmt.Sprintf("%d review findings remain", len(kept))
	}
	if len(kept) == 0 {
		out.RiskLevel = "low"
		out.RiskRationale = "no delivery-independent review risk was reported"
		out.RiskScope = types.FindingsRiskScopeSourceOrExternal
		return out, dropped
	}
	rank := 0
	if findings.RiskScope != types.FindingsRiskScopePipelineOwnedDelivery {
		rank = riskRank(findings.RiskLevel)
	}
	for _, item := range kept {
		if item.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery {
			continue
		}
		if itemRank := severityRank(item.Severity); itemRank > rank {
			rank = itemRank
		}
	}
	if rank == 0 {
		rank = riskRank("low")
	}
	out.RiskLevel = riskLevel(rank)
	out.RiskRationale = "review risk recomputed after deferred delivery filtering"
	out.RiskScope = types.FindingsRiskScopeSourceOrExternal
	return out, dropped
}

func normalizeUserFindingsJSON(raw, existingRaw string) (string, error) {
	if raw == "" {
		return raw, nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return "", err
	}
	var existing []types.Finding
	if existingRaw != "" {
		parsed, err := types.ParseFindingsJSON(existingRaw)
		if err != nil {
			return "", err
		}
		existing = parsed.Items
	}
	normalized, err := types.NormalizeUserFindings(findings, existing)
	if err != nil {
		return "", err
	}
	return types.MarshalFindingsJSON(normalized)
}

func countFindingIdentities(items []types.Finding) map[types.FindingIdentity]int {
	counts := make(map[types.FindingIdentity]int, len(items))
	for _, item := range items {
		counts[item.Identity()]++
	}
	return counts
}

func mergeEvidenceSummary(fresh, carried string) string {
	fresh = strings.TrimSpace(fresh)
	carried = strings.TrimSpace(carried)
	switch {
	case fresh == "":
		return carried
	case carried == "", carried == fresh:
		return fresh
	default:
		return fresh + "\n\n" + carried
	}
}

func mergeFindingEvidence(fresh, carried *types.FindingEvidence) *types.FindingEvidence {
	if fresh == nil && carried == nil {
		return nil
	}
	merged := &types.FindingEvidence{}
	if fresh != nil {
		merged.Tested = append(merged.Tested, fresh.Tested...)
		merged.TestingSummary = fresh.TestingSummary
		merged.Artifacts = append(merged.Artifacts, fresh.Artifacts...)
	}
	if carried != nil {
		merged.Tested = mergeComparable(merged.Tested, carried.Tested)
		merged.TestingSummary = mergeEvidenceSummary(merged.TestingSummary, carried.TestingSummary)
		merged.Artifacts = mergeComparable(merged.Artifacts, carried.Artifacts)
	}
	return merged
}

func rebuildAttributedEvidence(findings *types.Findings) bool {
	attributed := false
	var tested []string
	var testingSummary string
	var artifacts []types.TestArtifact
	for _, item := range findings.Items {
		if item.Evidence == nil {
			continue
		}
		attributed = true
		tested = mergeComparable(tested, item.Evidence.Tested)
		testingSummary = mergeEvidenceSummary(testingSummary, item.Evidence.TestingSummary)
		artifacts = mergeComparable(artifacts, item.Evidence.Artifacts)
	}
	if findings.SharedEvidence != nil {
		attributed = true
		tested = mergeComparable(tested, findings.SharedEvidence.Tested)
		testingSummary = mergeEvidenceSummary(testingSummary, findings.SharedEvidence.TestingSummary)
		artifacts = mergeComparable(artifacts, findings.SharedEvidence.Artifacts)
	}
	if attributed {
		findings.Tested = tested
		findings.TestingSummary = testingSummary
		findings.Artifacts = artifacts
	}
	return attributed
}

func residualSharedEvidence(findings types.Findings) *types.FindingEvidence {
	owned := &types.FindingEvidence{}
	for _, item := range findings.Items {
		owned = mergeFindingEvidence(owned, item.Evidence)
	}
	shared := &types.FindingEvidence{
		Tested:         subtractComparable(findings.Tested, owned.Tested),
		TestingSummary: subtractEvidenceSummary(findings.TestingSummary, owned.TestingSummary),
		Artifacts:      subtractComparable(findings.Artifacts, owned.Artifacts),
	}
	if len(shared.Tested) == 0 && shared.TestingSummary == "" && len(shared.Artifacts) == 0 {
		return &types.FindingEvidence{}
	}
	return shared
}

func sharedEvidenceOwner(findings types.Findings) *types.FindingEvidence {
	if findings.SharedEvidence != nil {
		return findings.SharedEvidence
	}
	if len(findings.Tested) == 0 && findings.TestingSummary == "" && len(findings.Artifacts) == 0 {
		return nil
	}
	return residualSharedEvidence(findings)
}

func subtractComparable[T comparable](aggregate, owned []T) []T {
	ownedSet := make(map[T]bool, len(owned))
	for _, value := range owned {
		ownedSet[value] = true
	}
	remaining := make([]T, 0, len(aggregate))
	for _, value := range aggregate {
		if !ownedSet[value] {
			remaining = append(remaining, value)
		}
	}
	return remaining
}

func subtractEvidenceSummary(aggregate, owned string) string {
	aggregate = strings.TrimSpace(aggregate)
	owned = strings.TrimSpace(owned)
	if aggregate == "" || aggregate == owned {
		return ""
	}
	if owned == "" {
		return aggregate
	}
	ownedBlocks := make(map[string]int)
	for _, block := range strings.Split(owned, "\n\n") {
		block = strings.TrimSpace(block)
		if block != "" {
			ownedBlocks[block]++
		}
	}
	var remaining []string
	for _, block := range strings.Split(aggregate, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if ownedBlocks[block] > 0 {
			ownedBlocks[block]--
			continue
		}
		remaining = append(remaining, block)
	}
	for _, count := range ownedBlocks {
		if count > 0 {
			return aggregate
		}
	}
	return strings.Join(remaining, "\n\n")
}

func effectiveFindingsRisk(items []types.Finding, fresh, carried types.Findings, carriedCount int) (string, string, string) {
	rank := 0
	if fresh.RiskScope != types.FindingsRiskScopePipelineOwnedDelivery {
		rank = riskRank(fresh.RiskLevel)
	}
	if carried.RiskScope != types.FindingsRiskScopePipelineOwnedDelivery {
		carriedRank := riskRank(carried.RiskLevel)
		if carriedRank > rank {
			rank = carriedRank
		}
	}
	excluded := 0
	for _, item := range items {
		if item.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery {
			excluded++
			continue
		}
		if severityRank(item.Severity) > rank {
			rank = severityRank(item.Severity)
		}
	}
	if rank == 0 {
		rank = riskRank("low")
	}
	rationale := fmt.Sprintf("Effective review contains %d unresolved %s, including %d carried from earlier review rounds.", len(items), pluralize(len(items), "finding", "findings"), carriedCount)
	if excluded > 0 {
		rationale += fmt.Sprintf(" %d pipeline-owned delivery %s excluded from source/external risk.", excluded, pluralize(excluded, "finding was", "findings were"))
	}
	return riskLevel(rank), rationale, types.FindingsRiskScopeSourceOrExternal
}

func severityRank(severity string) int {
	switch severity {
	case "error":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func riskRank(level string) int {
	switch level {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func riskLevel(rank int) string {
	switch rank {
	case 3:
		return "high"
	case 2:
		return "medium"
	case 1:
		return "low"
	default:
		return ""
	}
}

func mergeComparable[T comparable](fresh, carried []T) []T {
	seen := make(map[T]bool, len(fresh)+len(carried))
	merged := make([]T, 0, len(fresh)+len(carried))
	for _, values := range [][]T{fresh, carried} {
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				merged = append(merged, value)
			}
		}
	}
	return merged
}

func stricterFindingAction(carried, fresh string) string {
	if findingActionRank(fresh) > findingActionRank(carried) {
		return fresh
	}
	return carried
}

func findingActionRank(action string) int {
	switch action {
	case types.ActionNoOp:
		return 1
	case types.ActionAutoFix:
		return 2
	default:
		return 3
	}
}

func mergeFindingsJSON(existingRaw, additionalRaw string) string {
	if existingRaw == "" {
		return additionalRaw
	}
	if additionalRaw == "" {
		return existingRaw
	}
	existing, err := types.ParseFindingsJSON(existingRaw)
	if err != nil {
		return additionalRaw
	}
	additional, err := types.ParseFindingsJSON(additionalRaw)
	if err != nil {
		return existingRaw
	}
	existingIDs := types.StableFindingIDs(existing.Items)
	existingOccurrences := types.CountFindingOccurrences(existing.Items)
	additionalOccurrences := types.CountFindingOccurrences(additional.Items)
	existingCounts := types.CountFindingFingerprints(existing.Items)
	additionalCounts := types.CountFindingFingerprints(additional.Items)
	existingIdentityCounts := countFindingIdentities(existing.Items)
	additionalIdentityCounts := countFindingIdentities(additional.Items)
	merged := types.Findings{Summary: existing.Summary, Tested: existing.Tested, TestingSummary: existing.TestingSummary, Artifacts: existing.Artifacts, SharedEvidence: mergeFindingEvidence(sharedEvidenceOwner(existing), sharedEvidenceOwner(additional)), RiskLevel: existing.RiskLevel, RiskRationale: existing.RiskRationale, RiskScope: existing.RiskScope}
	for _, item := range existing.Items {
		merged.Items = append(merged.Items, item)
	}
	for _, item := range additional.Items {
		if hasFindingMatch(item, existingIDs, additionalOccurrences, existingOccurrences, additionalIdentityCounts, existingIdentityCounts, additionalCounts, existingCounts) {
			for i := range merged.Items {
				if types.FindingIDCorroborates(merged.Items[i], item) || types.FindingOccurrenceCorroborates(merged.Items[i], item) {
					merged.Items[i].Evidence = mergeFindingEvidence(merged.Items[i].Evidence, item.Evidence)
					break
				}
			}
			continue
		}
		merged.Items = append(merged.Items, item)
	}
	if len(merged.Items) == 0 {
		return ""
	}
	rebuildAttributedEvidence(&merged)
	mergedRaw, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return existingRaw
	}
	return mergedRaw
}

func removeMatchingFindingsJSON(existingRaw, removeRaw string) string {
	if existingRaw == "" || removeRaw == "" {
		return existingRaw
	}
	existing, err := types.ParseFindingsJSON(existingRaw)
	if err != nil {
		return existingRaw
	}
	remove, err := types.ParseFindingsJSON(removeRaw)
	if err != nil {
		return existingRaw
	}
	removeIDs := types.StableFindingIDs(remove.Items)
	removeOccurrences := types.CountFindingOccurrences(remove.Items)
	existingOccurrences := types.CountFindingOccurrences(existing.Items)
	existingCounts := types.CountFindingFingerprints(existing.Items)
	removeCounts := types.CountFindingFingerprints(remove.Items)
	existingIdentityCounts := countFindingIdentities(existing.Items)
	removeIdentityCounts := countFindingIdentities(remove.Items)
	filtered := types.Findings{Summary: existing.Summary, Tested: existing.Tested, TestingSummary: existing.TestingSummary, Artifacts: existing.Artifacts, SharedEvidence: existing.SharedEvidence, RiskLevel: existing.RiskLevel, RiskRationale: existing.RiskRationale, RiskScope: existing.RiskScope}
	for _, item := range existing.Items {
		if hasFindingMatch(item, removeIDs, existingOccurrences, removeOccurrences, existingIdentityCounts, removeIdentityCounts, existingCounts, removeCounts) {
			continue
		}
		filtered.Items = append(filtered.Items, item)
	}
	if len(filtered.Items) == 0 {
		return ""
	}
	if len(filtered.Items) != len(existing.Items) && !rebuildAttributedEvidence(&filtered) {
		filtered.Tested = nil
		filtered.TestingSummary = ""
		filtered.Artifacts = nil
	}
	filteredRaw, err := types.MarshalFindingsJSON(filtered)
	if err != nil {
		return existingRaw
	}
	return filteredRaw
}

func retainMatchingFindingsJSON(existingRaw, keepRaw string) string {
	if existingRaw == "" || keepRaw == "" {
		return ""
	}
	existing, err := types.ParseFindingsJSON(existingRaw)
	if err != nil {
		return ""
	}
	keep, err := types.ParseFindingsJSON(keepRaw)
	if err != nil {
		return ""
	}
	keepIDs := types.StableFindingIDs(keep.Items)
	keepOccurrences := types.CountFindingOccurrences(keep.Items)
	existingOccurrences := types.CountFindingOccurrences(existing.Items)
	existingCounts := types.CountFindingFingerprints(existing.Items)
	keepCounts := types.CountFindingFingerprints(keep.Items)
	existingIdentityCounts := countFindingIdentities(existing.Items)
	keepIdentityCounts := countFindingIdentities(keep.Items)
	filtered := types.Findings{Summary: existing.Summary, Tested: existing.Tested, TestingSummary: existing.TestingSummary, Artifacts: existing.Artifacts, SharedEvidence: existing.SharedEvidence, RiskLevel: existing.RiskLevel, RiskRationale: existing.RiskRationale, RiskScope: existing.RiskScope}
	for _, item := range existing.Items {
		if !hasFindingMatch(item, keepIDs, existingOccurrences, keepOccurrences, existingIdentityCounts, keepIdentityCounts, existingCounts, keepCounts) {
			continue
		}
		filtered.Items = append(filtered.Items, item)
	}
	if len(filtered.Items) == 0 {
		return ""
	}
	if len(filtered.Items) != len(existing.Items) && !rebuildAttributedEvidence(&filtered) {
		filtered.Tested = nil
		filtered.TestingSummary = ""
		filtered.Artifacts = nil
	}
	filteredRaw, err := types.MarshalFindingsJSON(filtered)
	if err != nil {
		return ""
	}
	return filteredRaw
}

func autoFixableFindingsJSON(raw string) string {
	if raw == "" {
		return ""
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	fixable := types.AutoFixableFindings(findings)
	if len(fixable.Items) == 0 {
		return ""
	}
	if len(fixable.Items) != len(findings.Items) {
		rebuildAttributedEvidence(&fixable)
	}
	fixableRaw, err := types.MarshalFindingsJSON(fixable)
	if err != nil {
		return raw
	}
	return fixableRaw
}

func hasAskUserFindingsJSON(raw string) bool {
	if raw == "" {
		return false
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return false
	}
	return types.HasAskUserFindings(findings)
}

// actionableFindingsCountJSON returns how many findings in raw carry an
// actionable action (anything other than "no-op"). Unparseable or empty
// findings count as zero.
func actionableFindingsCountJSON(raw string) int {
	if raw == "" {
		return 0
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return 0
	}
	return types.CountActionableFindings(findings)
}

// combineSelectedFindingIDs returns the ordered list of finding IDs that
// were dispatched to the fix agent: the user's selected agent-produced
// IDs plus any user-authored finding IDs (which only appear in the merged
// list).
func combineSelectedFindingIDs(selected []string, mergedFindings string) []string {
	if mergedFindings == "" {
		return selected
	}
	merged, err := types.ParseFindingsJSON(mergedFindings)
	if err != nil {
		return selected
	}
	seen := make(map[string]bool, len(selected))
	for _, id := range selected {
		if id != "" {
			seen[id] = true
		}
	}
	result := append([]string(nil), selected...)
	for _, item := range merged.Items {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		result = append(result, item.ID)
		seen[item.ID] = true
	}
	return result
}

// mergeUserOverridesJSON takes a findings JSON payload and applies
// per-finding user instructions and user-authored findings. When no
// overrides are present the input is returned unchanged.
func mergeUserOverridesJSON(raw string, instructions map[string]string, added []types.Finding) string {
	if len(instructions) == 0 && len(added) == 0 {
		return raw
	}
	base, err := types.ParseFindingsJSON(raw)
	if err != nil {
		base = types.Findings{}
	}
	merged := types.MergeUserOverrides(base, instructions, added)
	encoded, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return raw
	}
	return encoded
}

func prepareUserFixFindingsJSON(selected, known string, instructions map[string]string, added []types.Finding, registerLineages bool) (string, string, error) {
	merged := mergeUserOverridesJSON(selected, instructions, added)
	if !registerLineages {
		return merged, "", nil
	}
	normalized, err := normalizeUserFindingsJSON(merged, known)
	if err != nil {
		return "", "", err
	}
	return normalized, mergeFindingsJSON(normalized, known), nil
}

func filterFindingsJSON(raw string, ids []string) string {
	if raw == "" {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	filtered := types.FilterFindings(findings, ids)
	if len(filtered.Items) != len(findings.Items) {
		rebuildAttributedEvidence(&filtered)
	}
	if len(ids) == 0 {
		filtered = types.Findings{
			Summary:        "0 selected findings",
			Tested:         findings.Tested,
			TestingSummary: findings.TestingSummary,
			Artifacts:      findings.Artifacts,
			SharedEvidence: findings.SharedEvidence,
			RiskLevel:      findings.RiskLevel,
			RiskRationale:  findings.RiskRationale,
			RiskScope:      findings.RiskScope,
		}
	}
	filteredRaw, err := types.MarshalFindingsJSON(filtered)
	if err != nil {
		return raw
	}
	return filteredRaw
}
