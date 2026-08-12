// Package risk classifies a change before expensive validation begins.
package risk

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
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
	Path    string
	Status  ChangeStatus
	Added   int
	Deleted int
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
}

// Axis records one risk score and the evidence behind it.
type Axis struct {
	Score  int
	Reason string
}

// Decision is the printable classifier verdict.
type Decision struct {
	Tier          Tier
	BlastRadius   Axis
	Novelty       Axis
	Reversibility Axis
	Overridden    bool
	OriginalTier  Tier
}

// Classify chooses a validation tier from the change's reach, novelty, and
// reversibility.
func Classify(change ChangeSet, cfg Config) (Decision, error) {
	if len(change.Files) == 0 {
		return Decision{}, fmt.Errorf("classify change: no changed files")
	}
	if markdownOnly(change.Files) {
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
		return applyOverride(decision, cfg.OverrideTier)
	}

	decision := Decision{
		BlastRadius:   classifyBlastRadius(change.Files, cfg.HighRiskPaths),
		Novelty:       classifyNovelty(change.Files),
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
	return applyOverride(decision, cfg.OverrideTier)
}

func applyOverride(decision Decision, override Tier) (Decision, error) {
	if override == "" || override == "auto" {
		return decision, nil
	}
	switch override {
	case TierLeakScanOnly, TierSingleReview, TierFullAdversarial:
		decision.OriginalTier = decision.Tier
		decision.Tier = override
		decision.Overridden = true
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("classify change: invalid tier override %q", override)
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
	return Axis{Score: 2, Reason: "source code can affect runtime behavior"}
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
	if allChanges(files, func(file FileChange) bool { return file.Status == Renamed }) {
		return Axis{Score: 0, Reason: "change is a mechanical rename"}
	}
	return Axis{Score: 2, Reason: "existing source logic changed"}
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
		if predicate(file.Path) {
			return true
		}
	}
	return false
}

func allPaths(files []FileChange, predicate func(string) bool) bool {
	for _, file := range files {
		if !predicate(file.Path) {
			return false
		}
	}
	return true
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
		if matchConfiguredPath(lower, pattern) {
			return true
		}
	}
	return dependencyPath(lower)
}

func hardToReversePath(name string) bool {
	lower := strings.ToLower(filepath.ToSlash(name))
	return dependencyPath(lower) || strings.Contains(lower, "migration") || strings.HasPrefix(lower, ".github/workflows/") || strings.HasPrefix(lower, "deploy/") || strings.HasPrefix(lower, "infra/") || strings.HasPrefix(lower, "terraform/")
}

func matchConfiguredPath(name, pattern string) bool {
	pattern = strings.ToLower(filepath.ToSlash(strings.TrimSpace(pattern)))
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return name == prefix || strings.HasPrefix(name, prefix+"/")
	}
	matched, _ := path.Match(pattern, name)
	return matched
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
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file.Path))
		if ext != ".md" && ext != ".mdx" {
			return false
		}
	}
	return true
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
	if d.Overridden {
		printed += fmt.Sprintf("\noverride: %s -> %s", d.OriginalTier, d.Tier)
	}
	return printed
}
