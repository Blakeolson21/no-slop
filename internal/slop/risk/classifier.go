// Package risk classifies a change before expensive validation begins.
package risk

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/slop/pathmatch"
	"github.com/kunchenguid/no-mistakes/internal/slop/provenance"
)

// Tier names the validation depth selected for a change.
type Tier string

const (
	TierLeakScanOnly    Tier = "leak-scan-only"
	TierSingleReview    Tier = "single-review"
	TierFullAdversarial Tier = "full-adversarial"
)

// ChangeStatus describes how a path changed.
type ChangeStatus string

const (
	Added    ChangeStatus = "added"
	Modified ChangeStatus = "modified"
	Deleted  ChangeStatus = "deleted"
	Renamed  ChangeStatus = "renamed"
)

// FileChange is the classifier's path-level input.
type FileChange struct {
	Path            string
	BaselinePath    string
	Status          ChangeStatus
	Added           int
	Deleted         int
	BaselineContent string
	BaselineContext string
	CurrentContent  string
}

// ChangeSet describes the complete diff and where it will land.
type ChangeSet struct {
	Branch        string
	DefaultBranch string
	Files         []FileChange
}

// Config controls tier thresholds and path rules.
type Config struct {
	SingleReviewThreshold int
	FullReviewThreshold   int
	HighRiskPaths         []string
	OverrideTier          Tier
	ForceTier             bool
	ProvenanceStore       provenance.Reader
	AgentLaneID           string
	Model                 string
}

// Axis records one risk score and the evidence behind it.
type Axis struct {
	Score  int
	Reason string
}

// Decision is the printable classifier verdict.
type Decision struct {
	Tier                Tier
	BlastRadius         Axis
	Novelty             Axis
	Reversibility       Axis
	Overridden          bool
	OverrideRefused     bool
	OverrideForced      bool
	OriginalTier        Tier
	RequestedTier       Tier
	Rationale           string
	ProvenanceEscalated bool
	PriorityLenses      []string
	DeterministicProbes []string
}

// Classify chooses a validation tier from the change's reach, novelty, and
// reversibility.
func Classify(change ChangeSet, cfg Config) (Decision, error) {
	if len(change.Files) == 0 {
		return Decision{}, fmt.Errorf("classify change: no changed files")
	}
	highRisk := anyPath(change.Files, func(name string) bool { return highRiskPath(name, cfg.HighRiskPaths) })
	if markdownOnly(change.Files) && !highRisk {
		reversibility := "content-only change is straightforward to revert"
		if change.DefaultBranch != "" && change.Branch == change.DefaultBranch {
			reversibility = "content-only change remains straightforward to revert on the default branch"
		}
		decision := Decision{
			Tier:          TierLeakScanOnly,
			BlastRadius:   Axis{Score: 0, Reason: "Markdown-only changes do not reach runtime code"},
			Novelty:       Axis{Score: 0, Reason: "Markdown-only content change"},
			Reversibility: Axis{Score: 0, Reason: reversibility},
		}
		return finalizeDecision(decision, cfg)
	}

	novelty := classifyNovelty(change.Files)
	if markdownOnly(change.Files) && highRisk {
		novelty = Axis{Score: 2, Reason: "high-risk instructions or content behavior changed"}
	}
	decision := Decision{
		BlastRadius:   classifyBlastRadius(change.Files, cfg.HighRiskPaths),
		Novelty:       novelty,
		Reversibility: classifyReversibility(change, cfg.HighRiskPaths),
	}
	singleThreshold, fullThreshold := thresholds(cfg)
	total := decision.BlastRadius.Score + decision.Novelty.Score + decision.Reversibility.Score
	switch {
	case total >= fullThreshold:
		decision.Tier = TierFullAdversarial
	case total >= singleThreshold:
		decision.Tier = TierSingleReview
	default:
		decision.Tier = TierLeakScanOnly
	}
	return finalizeDecision(decision, cfg)
}

func finalizeDecision(decision Decision, cfg Config) (Decision, error) {
	decision = conditionOnProvenance(decision, cfg)
	return applyOverride(decision, cfg.OverrideTier, cfg.ForceTier)
}

func conditionOnProvenance(decision Decision, cfg Config) Decision {
	laneID := strings.TrimSpace(cfg.AgentLaneID)
	model := strings.TrimSpace(cfg.Model)
	switch {
	case cfg.ProvenanceStore == nil:
		decision.Rationale = "no provenance history configured; using v1 policy"
		return decision
	case laneID == "" || model == "" || laneID == "unknown" || model == "unknown":
		decision.Rationale = "no lane/model provenance key; using v1 policy"
		return decision
	}
	history, err := cfg.ProvenanceStore.Recent(laneID, model, 10)
	if err != nil {
		decision.Tier = TierFullAdversarial
		decision.ProvenanceEscalated = true
		decision.Rationale = fmt.Sprintf("history could not be read for lane %s and model %s; escalating to full-adversarial", laneID, model)
		return decision
	}
	if len(history) == 0 {
		decision.Rationale = fmt.Sprintf("no history for lane %s and model %s; using v1 policy", laneID, model)
		return decision
	}

	scores := make(map[string]int)
	for _, record := range history {
		for lens, findings := range record.FindingsByLens {
			scores[lens] += len(findings.Accepted) - len(findings.Rejected)
		}
	}
	for lens, score := range scores {
		if score >= 3 {
			decision.PriorityLenses = append(decision.PriorityLenses, lens)
		}
	}
	sort.Slice(decision.PriorityLenses, func(left, right int) bool {
		leftScore := scores[decision.PriorityLenses[left]]
		rightScore := scores[decision.PriorityLenses[right]]
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return decision.PriorityLenses[left] < decision.PriorityLenses[right]
	})
	if len(decision.PriorityLenses) == 0 {
		decision.Rationale = fmt.Sprintf("lane %s: no repeated lens threshold in last %d changes; keeping v1 policy", laneID, len(history))
		return decision
	}

	primary := decision.PriorityLenses[0]
	decision.Tier = raiseTier(decision.Tier)
	decision.ProvenanceEscalated = true
	decision.Rationale = fmt.Sprintf("lane %s: %d %s findings in last %d changes, escalating", laneID, scores[primary], primary, len(history))
	for _, lens := range decision.PriorityLenses {
		if lens == "test-capitulation" {
			decision.DeterministicProbes = append(decision.DeterministicProbes, "test-count-floor")
		}
	}
	return decision
}

func raiseTier(tier Tier) Tier {
	switch tier {
	case TierLeakScanOnly:
		return TierSingleReview
	case TierSingleReview, TierFullAdversarial:
		return TierFullAdversarial
	default:
		return TierFullAdversarial
	}
}

func applyOverride(decision Decision, override Tier, force bool) (Decision, error) {
	if override == "" || override == "auto" {
		return decision, nil
	}
	switch override {
	case TierLeakScanOnly, TierSingleReview, TierFullAdversarial:
		if override == decision.Tier {
			return decision, nil
		}
		if decision.ProvenanceEscalated && tierRank(override) < tierRank(decision.Tier) && !force {
			decision.OverrideRefused = true
			decision.OriginalTier = decision.Tier
			decision.RequestedTier = override
			return decision, fmt.Errorf("classify change: --tier %s contradicts provenance-driven escalation to %s; use --force-tier to accept the lower tier", override, decision.Tier)
		}
		decision.OriginalTier = decision.Tier
		decision.RequestedTier = override
		decision.Tier = override
		decision.Overridden = true
		decision.OverrideForced = force && decision.ProvenanceEscalated && tierRank(override) < tierRank(decision.OriginalTier)
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("classify change: invalid tier override %q", override)
	}
}

func tierRank(tier Tier) int {
	switch tier {
	case TierLeakScanOnly:
		return 1
	case TierSingleReview:
		return 2
	case TierFullAdversarial:
		return 3
	default:
		return 0
	}
}

func thresholds(cfg Config) (int, int) {
	single := cfg.SingleReviewThreshold
	if single <= 0 {
		single = 3
	}
	full := cfg.FullReviewThreshold
	if full <= single {
		full = single + 3
	}
	return single, full
}

func classifyBlastRadius(files []FileChange, configured []string) Axis {
	if anyPath(files, func(name string) bool { return highRiskPath(name, configured) }) {
		return Axis{Score: 3, Reason: "change touches a high-reach runtime or delivery path"}
	}
	if allPaths(files, isTestOrDocsPath) {
		return Axis{Score: 1, Reason: "change is limited to tests, documentation, or examples"}
	}
	if substantialSourceAddition(files) {
		return Axis{Score: 3, Reason: "substantial new source has broad integration reach"}
	}
	return Axis{Score: 2, Reason: "source code can affect runtime behavior"}
}

func substantialSourceAddition(files []FileChange) bool {
	const substantialSourceLines = 500
	added := 0
	for _, file := range files {
		if file.Status == Added && sourcePath(file.Path) && !isTestOrDocsPath(file.Path) {
			added += file.Added
		}
	}
	return added >= substantialSourceLines
}

func classifyNovelty(files []FileChange) Axis {
	for _, file := range files {
		if file.Status == Added && (file.Added >= 50 || sourcePath(file.Path)) {
			return Axis{Score: 3, Reason: "change introduces a new source artifact or substantial new logic"}
		}
	}
	if allPaths(files, func(name string) bool { return !sourcePath(name) }) {
		return Axis{Score: 1, Reason: "change adjusts non-runtime artifacts"}
	}
	if allChanges(files, func(file FileChange) bool {
		return file.Status == Renamed && relocationPreservesCategory(file)
	}) {
		return Axis{Score: 0, Reason: "change is a mechanical rename"}
	}
	if allChanges(files, mechanicallyEquivalent) {
		return Axis{Score: 0, Reason: "source token stream contains only consistent identifier substitutions"}
	}
	return Axis{Score: 2, Reason: "existing source logic changed"}
}

type sourceToken struct {
	kind byte
	text string
}

func mechanicallyEquivalent(file FileChange) bool {
	if file.Status == Renamed {
		return relocationPreservesCategory(file)
	}
	if file.Status != Modified || !relocationPreservesCategory(file) || !fileMatchesPath(file, sourcePath) || file.BaselineContent == "" || file.CurrentContent == "" {
		return false
	}
	baseline := sourceTokens(file.BaselineContent)
	current := sourceTokens(file.CurrentContent)
	if len(baseline) != len(current) {
		return false
	}
	baselineIdentifiers := make(map[string]struct{})
	for _, token := range sourceTokens(file.BaselineContent + "\n" + file.BaselineContext) {
		if token.kind == 'i' {
			baselineIdentifiers[token.text] = struct{}{}
		}
	}
	forward := make(map[string]string)
	reverse := make(map[string]string)
	changed := false
	for index := range baseline {
		left, right := baseline[index], current[index]
		if left.text == right.text {
			continue
		}
		changed = true
		if left.kind != 'i' || right.kind != 'i' || sourceKeyword(left.text) || sourceKeyword(right.text) {
			return false
		}
		if _, collision := baselineIdentifiers[right.text]; collision {
			return false
		}
		if identifierControlsCallTarget(baseline, current, index) {
			return false
		}
		if mapped, ok := forward[left.text]; ok && mapped != right.text {
			return false
		}
		if mapped, ok := reverse[right.text]; ok && mapped != left.text {
			return false
		}
		forward[left.text] = right.text
		reverse[right.text] = left.text
	}
	return changed || file.BaselineContent != file.CurrentContent
}

func relocationPreservesCategory(file FileChange) bool {
	if file.BaselinePath == "" {
		return true
	}
	return pathCategory(file.Path) == pathCategory(file.BaselinePath)
}

func pathCategory(path string) int {
	switch {
	case isTestOrDocsPath(path):
		return 1
	case sourcePath(path):
		return 2
	default:
		return 0
	}
}

func identifierControlsCallTarget(baseline, current []sourceToken, index int) bool {
	if index > 0 && (baseline[index-1].text == "." || current[index-1].text == ".") {
		return true
	}
	if index+1 >= len(baseline) || baseline[index+1].text != "(" || current[index+1].text != "(" {
		return false
	}
	return index == 0 || (baseline[index-1].text != "func" && current[index-1].text != "func")
}

func sourceTokens(content string) []sourceToken {
	var tokens []sourceToken
	for index := 0; index < len(content); {
		current := content[index]
		switch {
		case isSpace(current):
			index++
		case isIdentifierStart(current):
			end := index + 1
			for end < len(content) && isIdentifierPart(content[end]) {
				end++
			}
			tokens = append(tokens, sourceToken{kind: 'i', text: content[index:end]})
			index = end
		case current == '"' || current == '\'' || current == '`':
			end := quotedTokenEnd(content, index, current)
			tokens = append(tokens, sourceToken{kind: 'l', text: content[index:end]})
			index = end
		case current >= '0' && current <= '9':
			end := index + 1
			for end < len(content) && (isIdentifierPart(content[end]) || content[end] == '.') {
				end++
			}
			tokens = append(tokens, sourceToken{kind: 'l', text: content[index:end]})
			index = end
		default:
			tokens = append(tokens, sourceToken{kind: 'p', text: content[index : index+1]})
			index++
		}
	}
	return tokens
}

func quotedTokenEnd(content string, start int, quote byte) int {
	for index := start + 1; index < len(content); index++ {
		if quote != '`' && content[index] == '\\' {
			index++
			continue
		}
		if content[index] == quote {
			return index + 1
		}
	}
	return len(content)
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isIdentifierPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9'
}

func sourceKeyword(value string) bool {
	_, ok := sourceKeywords[strings.ToLower(value)]
	return ok
}

var sourceKeywords = map[string]struct{}{
	"and": {}, "as": {}, "async": {}, "await": {}, "break": {}, "case": {}, "catch": {},
	"class": {}, "const": {}, "continue": {}, "def": {}, "default": {}, "defer": {}, "do": {},
	"else": {}, "enum": {}, "except": {}, "export": {}, "extends": {}, "false": {}, "finally": {},
	"fn": {}, "for": {}, "from": {}, "func": {}, "function": {}, "go": {}, "if": {}, "implements": {},
	"import": {}, "in": {}, "interface": {}, "is": {}, "let": {}, "map": {}, "match": {}, "new": {},
	"nil": {}, "none": {}, "not": {}, "null": {}, "or": {}, "package": {}, "pass": {}, "private": {},
	"protected": {}, "public": {}, "return": {}, "select": {}, "static": {}, "struct": {}, "super": {},
	"switch": {}, "this": {}, "throw": {}, "trait": {}, "true": {}, "try": {}, "type": {}, "var": {},
	"while": {}, "with": {}, "yield": {},
}

func classifyReversibility(change ChangeSet, configured []string) Axis {
	if change.DefaultBranch != "" && change.Branch == change.DefaultBranch {
		return Axis{Score: 3, Reason: "change is applied directly to the default branch"}
	}
	if anyPath(change.Files, hardToReversePath) {
		return Axis{Score: 2, Reason: "dependency, migration, or delivery changes can outlive a source revert"}
	}
	if anyPath(change.Files, func(name string) bool { return highRiskPath(name, configured) }) {
		return Axis{Score: 1, Reason: "high-reach behavior needs a guarded rollback even on a feature branch"}
	}
	return Axis{Score: 0, Reason: "change is isolated on a non-default branch"}
}

func anyPath(files []FileChange, predicate func(string) bool) bool {
	for _, file := range files {
		if fileMatchesPath(file, predicate) {
			return true
		}
	}
	return false
}

func allPaths(files []FileChange, predicate func(string) bool) bool {
	for _, file := range files {
		if !predicate(file.Path) || file.BaselinePath != "" && !predicate(file.BaselinePath) {
			return false
		}
	}
	return true
}

func fileMatchesPath(file FileChange, predicate func(string) bool) bool {
	return predicate(file.Path) || file.BaselinePath != "" && predicate(file.BaselinePath)
}

func allChanges(files []FileChange, predicate func(FileChange) bool) bool {
	for _, file := range files {
		if !predicate(file) {
			return false
		}
	}
	return true
}

func sourcePath(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".rs", ".py", ".rb", ".java", ".kt", ".js", ".jsx", ".ts", ".tsx", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".swift":
		return true
	default:
		return false
	}
}

func isTestOrDocsPath(name string) bool {
	lower := strings.ToLower(filepath.ToSlash(name))
	return strings.HasPrefix(lower, "docs/") ||
		strings.HasPrefix(lower, "examples/") ||
		strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/tests/") ||
		strings.HasSuffix(lower, "_test.go") ||
		strings.Contains(lower, ".test.") ||
		strings.Contains(lower, ".spec.") ||
		strings.HasPrefix(filepath.Base(lower), "test_")
}

func highRiskPath(name string, configured []string) bool {
	lower := strings.ToLower(filepath.ToSlash(name))
	for _, marker := range []string{
		"/auth/", "/security/", "/permission", "/credential", "/session", "/payment", "/billing/",
		"migrations/", ".github/workflows/", "deploy/", "infra/", "terraform/",
	} {
		if strings.Contains("/"+lower, marker) {
			return true
		}
	}
	for _, pattern := range configured {
		if pathmatch.Match(lower, pattern) {
			return true
		}
	}
	return dependencyPath(lower)
}

func hardToReversePath(name string) bool {
	lower := strings.ToLower(filepath.ToSlash(name))
	return dependencyPath(lower) || strings.Contains(lower, "migration") || strings.HasPrefix(lower, ".github/workflows/") || strings.HasPrefix(lower, "deploy/") || strings.HasPrefix(lower, "infra/") || strings.HasPrefix(lower, "terraform/")
}

func dependencyPath(lower string) bool {
	base := filepath.Base(lower)
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "cargo.toml", "cargo.lock", "requirements.txt", "poetry.lock", "gemfile", "gemfile.lock":
		return true
	default:
		return false
	}
}

func markdownOnly(files []FileChange) bool {
	return allPaths(files, func(path string) bool {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".mdx" {
			return false
		}
		return true
	})
}

// String renders the tier and all axis reasons. The decision is intentionally
// verbose enough to make silent routing impossible.
func (d Decision) String() string {
	printed := fmt.Sprintf(
		"tier: %s\nblast radius: %d, %s\nnovelty: %d, %s\nreversibility: %d, %s",
		d.Tier,
		d.BlastRadius.Score,
		d.BlastRadius.Reason,
		d.Novelty.Score,
		d.Novelty.Reason,
		d.Reversibility.Score,
		d.Reversibility.Reason,
	)
	if d.OverrideRefused {
		printed += fmt.Sprintf("\noverride refused: %s -> %s", d.OriginalTier, d.RequestedTier)
	} else if d.OverrideForced {
		printed += fmt.Sprintf("\noverride forced: %s -> %s", d.OriginalTier, d.Tier)
	} else if d.Overridden {
		printed += fmt.Sprintf("\noverride: %s -> %s", d.OriginalTier, d.Tier)
	}
	printed += "\nprovenance: " + d.Rationale
	if len(d.PriorityLenses) > 0 {
		printed += "\nlens priority: " + strings.Join(d.PriorityLenses, ", ")
	}
	if len(d.DeterministicProbes) > 0 {
		printed += "\ndeterministic probes: " + strings.Join(d.DeterministicProbes, ", ")
	}
	return printed
}
