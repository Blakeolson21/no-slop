// Package risk classifies a change before expensive validation begins.
package risk

import (
	"fmt"
	"go/build/constraint"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/slop/pathmatch"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
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
	Path         string
	BaselinePath string
	Status       ChangeStatus
	Added        int
	Deleted      int
	// BaselineContent and CurrentContent are the whole blob on each side.
	BaselineContent string
	CurrentContent  string
	// BaselineContext is neighbouring baseline source the collision check
	// reads. BaselineContextTruncated says the loader stopped short of the
	// full set, which makes every collision answer from it unsound, so the
	// classifier refuses to call the change mechanical rather than trusting a
	// partial view.
	BaselineContext          string
	BaselineContextTruncated bool
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
	// UnscannableContent names paths whose content the gate structurally
	// cannot read, a submodule gitlink being the only current case. Their
	// presence raises the tier: nothing this run can do will inspect them, so
	// the answer has to come from a reviewer.
	UnscannableContent []string
	// BaseUnverified says the canonical base could not be established from the
	// operator's remote. Gate strength is read from the base ref, so a run that
	// does not know which history is the operator's is pinned to the full tier
	// and can be lowered by nothing.
	BaseUnverified bool
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
	OriginalTier        Tier
	RequestedTier       Tier
	Rationale           string
	ProvenanceEscalated bool
	PriorityLenses      []string
	DeterministicProbes []string
	// Escalations names every reason the tier was raised after scoring, in the
	// order the raises were applied. They print on their own lines so a raise
	// is never something a reader has to infer from the tier alone.
	Escalations []string
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
			Tier: TierLeakScanOnly,
			// The reason states what was observed, not what it implies. The
			// previous wording asserted that Markdown does not reach runtime
			// code, which is false in an agent repository and was false in the
			// exact case that mattered: an instruction file. Instruction files
			// now take the high-risk branch above, so this branch really is
			// ordinary prose, and it says only that.
			BlastRadius:   Axis{Score: 0, Reason: "no runtime source file changed and no instruction surface matched"},
			Novelty:       Axis{Score: 0, Reason: "Markdown-only content change"},
			Reversibility: Axis{Score: 0, Reason: reversibility},
		}
		return finalizeDecision(decision, cfg)
	}

	novelty := classifyNovelty(change.Files, cfg.HighRiskPaths)
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

// finalizeDecision applies every raise the scored axes do not carry, then the
// caller's tier request, which may only ever raise. Order matters only in that
// nothing here can lower: each step takes the maximum of what it holds and what
// it is given, so their sequence cannot change the outcome.
func finalizeDecision(decision Decision, cfg Config) (Decision, error) {
	decision = conditionOnProvenance(decision, cfg)
	decision = raiseForUnscannableContent(decision, cfg)
	decision = pinUnverifiedBaseToFullTier(decision, cfg)
	return applyOverride(decision, cfg.OverrideTier, cfg.ForceTier)
}

// raiseForUnscannableContent raises the tier once for content this gate cannot
// read at all. A submodule pointer bump is the case: its content is in another
// repository, so every mechanical check here is structurally blind to it and
// the only honest answer is a reviewer's. This used to be an unconditional
// failure, which no repository using submodules could ever clear.
func raiseForUnscannableContent(decision Decision, cfg Config) Decision {
	if len(cfg.UnscannableContent) == 0 {
		return decision
	}
	raised := raiseTier(decision.Tier)
	decision.Escalations = append(decision.Escalations, fmt.Sprintf(
		"content this run cannot scan (%s) raises the tier to %s",
		strings.Join(cfg.UnscannableContent, ", "), raised))
	decision.Tier = raised
	return decision
}

// pinUnverifiedBaseToFullTier pins a run whose canonical base could not be
// verified against the operator's remote to the full tier.
//
// Every gate-strength value is read from the base ref, so a run that cannot
// establish which history is the operator's has no trustworthy source for any
// of them. The answer is the S1 answer: the cheap routes are removed rather
// than constrained. This is a pin and not a raise, so no threshold, no
// provenance rationale, and no flag reaches below it.
func pinUnverifiedBaseToFullTier(decision Decision, cfg Config) Decision {
	if !cfg.BaseUnverified {
		return decision
	}
	if decision.Tier != TierFullAdversarial {
		decision.Escalations = append(decision.Escalations,
			"the canonical base could not be verified against the operator's remote, so the tier is pinned to full-adversarial")
		decision.Tier = TierFullAdversarial
		return decision
	}
	decision.Escalations = append(decision.Escalations,
		"the canonical base could not be verified against the operator's remote; the tier was already full-adversarial")
	return decision
}

func conditionOnProvenance(decision Decision, cfg Config) Decision {
	laneID := strings.TrimSpace(cfg.AgentLaneID)
	model := strings.TrimSpace(cfg.Model)
	if cfg.ProvenanceStore == nil {
		decision.Rationale = "no provenance history configured; using v1 policy"
		return decision
	}
	// Nothing authenticates --lane-id or --model. They are strings the audited
	// party writes about itself, so every identity path has to cost the same,
	// or the cheapest one becomes the policy.
	//
	// The first version punished OMISSION only, on the reasoning that omitting
	// is an evasion where identified history exists to hide from. That reasoning
	// is right and the rule built from it was still backwards: asserting a lane
	// the store has never seen took the v1 route, so `--lane-id lane-zzz`
	// cleared a live escalation that omitting the flag could not. Silence was
	// punished and lying was free, which inverts the incentive the omission rule
	// exists to create.
	//
	// So on a store that already records identities, an unverifiable key and an
	// absent one are the same thing: an omitted lane, an "unknown" lane, and a
	// first-contact lane all escalate. A repository that has never recorded an
	// identity has no history to hide from and keeps the v1 route on every one
	// of those paths, which is the same symmetry read the other way.
	identified, err := cfg.ProvenanceStore.HasIdentifiedHistory()
	if err != nil {
		decision.Tier = TierFullAdversarial
		decision.ProvenanceEscalated = true
		decision.Rationale = "provenance history could not be read; escalating to full-adversarial"
		return decision
	}
	if laneID == "" || model == "" || laneID == "unknown" || model == "unknown" {
		if identified {
			decision.Tier = TierFullAdversarial
			decision.ProvenanceEscalated = true
			decision.Rationale = "no lane/model provenance key on a repository that records them; escalating to full-adversarial"
			return decision
		}
		decision.Rationale = "no lane/model provenance key and no identified history; using v1 policy"
		return decision
	}
	history, err := cfg.ProvenanceStore.Window(laneID, model)
	if err != nil {
		decision.Tier = TierFullAdversarial
		decision.ProvenanceEscalated = true
		decision.Rationale = fmt.Sprintf("history could not be read for lane %s and model %s; escalating to full-adversarial", laneID, model)
		return decision
	}
	if len(history) == 0 {
		if identified {
			decision.Tier = TierFullAdversarial
			decision.ProvenanceEscalated = true
			decision.Rationale = fmt.Sprintf("lane %s and model %s have no records on a repository that already records identities, and a self-asserted key with nothing behind it is treated exactly like an omitted one; escalating to full-adversarial", laneID, model)
			return decision
		}
		decision.Rationale = fmt.Sprintf("no history for lane %s and model %s and no identified history anywhere in the store; using v1 policy", laneID, model)
		return decision
	}

	scores := provenance.LensScores(history)
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
		decision.Rationale = fmt.Sprintf("lane %s: no repeated lens threshold across %d retained changes; keeping v1 policy", laneID, len(history))
		return decision
	}

	primary := decision.PriorityLenses[0]
	decision.Tier = raiseTier(decision.Tier)
	decision.ProvenanceEscalated = true
	decision.Rationale = fmt.Sprintf("lane %s: %d %s findings across %d retained changes, escalating", laneID, scores[primary], primary, len(history))
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

// applyOverride applies a requested tier. The flag may only ever RAISE one.
//
// The gate's promise has to hold when the author of the change is the party
// invoking it, and on this command line the caller and the audited party are
// the same agent. `--tier leak-scan-only` lowered any tier that provenance had
// not already raised, so one flag carried both structural invariant classes,
// an authorization weakening and a fleet-instruction rewrite, to "verdict:
// pass" at exit 0; `--force-tier` did the same over a live escalation. Printing
// the override was honest and changed nothing about the outcome.
//
// So neither flag can lower a computed tier, for any reason, and the refusal
// names the tier the classifier actually computed so the caller cannot claim
// not to have been told. An operator who genuinely needs a cheaper route
// configures `slop.risk.*` thresholds in the repository config, which is read
// from the BASE ref and is therefore already outside the blast radius of the
// change under test. There is deliberately no command-line equivalent.
func applyOverride(decision Decision, override Tier, force bool) (Decision, error) {
	if override == "" || override == "auto" {
		return decision, nil
	}
	switch override {
	case TierLeakScanOnly, TierSingleReview, TierFullAdversarial:
		if override == decision.Tier {
			return decision, nil
		}
		if tierRank(override) < tierRank(decision.Tier) {
			decision.OverrideRefused = true
			decision.OriginalTier = decision.Tier
			decision.RequestedTier = override
			return decision, fmt.Errorf(
				"classify change: --tier %s would lower the computed tier %s; the tier flag may only raise it%s. Configure slop.risk thresholds at the base ref if a cheaper route is genuinely correct for this repository",
				override, decision.Tier, lowerRefusalDetail(decision, force))
		}
		decision.OriginalTier = decision.Tier
		decision.RequestedTier = override
		decision.Tier = override
		decision.Overridden = true
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("classify change: invalid tier override %q", override)
	}
}

// lowerRefusalDetail names the two things a caller reaching this refusal is
// most likely to be about to try next.
func lowerRefusalDetail(decision Decision, force bool) string {
	detail := ""
	if decision.ProvenanceEscalated {
		detail += ", and this tier came from provenance escalation"
	}
	if force {
		detail += ", and --force-tier no longer lowers a computed tier"
	}
	return detail
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

// substantialSourceAddition reports a diff large enough that its integration
// reach is broad regardless of where it landed.
//
// Two things it deliberately does not key on, because the author picks both.
// It does not require the file to be newly created: identical logic appended to
// an existing file has the same reach as the same logic in a new file, and
// keying on creation let an author drop two tiers by choosing where to paste.
// It does not trust physical line count alone: line breaks are a formatting
// property, so a generated file holding thousands of declarations on a few
// hundred lines scored as a small change. Net new declarations answer the
// second, and either signal is enough.
func substantialSourceAddition(files []FileChange) bool {
	const (
		substantialSourceLines        = 500
		substantialSourceDeclarations = 40
	)
	added := 0
	declarations := 0
	for _, file := range files {
		if file.Status == Deleted || !sourcePath(file.Path) || isTestOrDocsPath(file.Path) {
			continue
		}
		added += file.Added
		declarations += netNewDeclarationCount(file)
	}
	return added >= substantialSourceLines || declarations >= substantialSourceDeclarations
}

func netNewDeclarationCount(file FileChange) int {
	lang := sourceLanguage(file.Path)
	net := len(declaredNames(file.CurrentContent, lang)) - len(declaredNames(file.BaselineContent, lang))
	if net < 0 {
		return 0
	}
	return net
}

func classifyNovelty(files []FileChange, highRiskPaths []string) Axis {
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
	// A high-risk path in the diff forfeits the mechanical route outright. The
	// cheap tier is for edits whose meaning a token comparison can settle, and
	// on the paths this repository calls high-risk the token stream IS the
	// runtime: an instruction file, a skill, or the gate's own config means
	// something different after a rename even when every token maps one to one.
	// Most such paths already fell out because they are not source, which made
	// the exemption look complete; a source file under a high-risk glob did not.
	if anyPath(files, func(name string) bool { return highRiskPath(name, highRiskPaths) }) {
		return Axis{Score: 2, Reason: "a high-risk path changed, so the mechanical-substitution route does not apply"}
	}
	scope := newRenameScope(files)
	if allChanges(files, func(file FileChange) bool { return mechanicallyEquivalent(file, scope) }) {
		return Axis{Score: 0, Reason: "source token stream contains only consistent identifier substitutions"}
	}
	return Axis{Score: 2, Reason: "existing source logic changed"}
}

// renamePair is one declaration transition a single changed file performs: the
// name it stopped declaring, and the name it started declaring in that name's
// place. A rename campaign leaves exactly these; a decoy leaves something else.
type renamePair struct {
	previous string
	current  string
}

// familyScope is the rename evidence available to one language family.
type familyScope struct {
	// localRenames holds every declaration transition each file performs on
	// itself, keyed by path. It is the ONLY evidence that counts. There is
	// deliberately no cross-file map: see renameScope.
	localRenames map[string]map[renamePair]struct{}
	// headUses holds every identifier that still appears at head anywhere in
	// this family's changed files, commentary included. A name a rename was
	// supposed to retire but which survives somewhere is not evidence of a
	// rename, so the mechanical route is refused rather than guessed at.
	headUses map[string]struct{}
}

// renameScope is the evidence that separates a rename from a substitution, and
// the history of this rule is the argument for how narrow it now is.
//
// Version one asked whether the substituted name collided with anything in the
// same directory. Every symbol declared elsewhere was invisible, so a guard
// swap across a package boundary read as a rename to a fresh name.
//
// Version two asked whether the change set DECLARED the new name at head. That
// is free to manufacture: any second file renaming anything at all to the
// target name supplied it, in any language, and so did a comment shaped like a
// declaration. Six probes in five languages passed an authorization weakening.
//
// Version three asked for a declaration TRANSITION carried by some file in the
// same language family, with only referenceable declarations qualifying. That
// killed all six probes and cost one throwaway file to defeat: `tools/dead.js`
// renaming its own dead `requireAdmin` to `allowAnyone` vouched for
// `src/handler.js` swapping a guard it imports from `lib/policy.js`, which the
// diff never touches. Confirmed in JavaScript and Python at novelty 0, exit 0.
//
// Each version constrained the evidence and the control moved one layer down,
// because the question the rule can actually answer is about names and the
// question that matters is about resolution. Deciding whether `src/handler.js`
// means the `allowAnyone` in `lib/policy.js` or the one in `tools/dead.js`
// requires resolving the reference, which a token stream cannot do in any
// language, let alone all of them.
//
// So the cross-file route is REMOVED rather than constrained again. A file may
// follow only the transitions it performs on itself, where the binding really
// is the same binding and no resolution question arises. A genuine rename
// campaign that spans files now scores as changed logic and buys one review
// round; that cost was weighed and accepted, against a bypass that cost one
// file and carried an authorization weakening to a passing verdict.
type renameScope struct {
	families map[string]*familyScope
}

func newRenameScope(files []FileChange) renameScope {
	scope := renameScope{families: make(map[string]*familyScope)}
	for _, file := range files {
		if !fileMatchesPath(file, sourcePath) {
			continue
		}
		lang := sourceLanguage(file.Path)
		family := scope.family(lang.family)
		if file.Status != Deleted {
			for _, token := range sourceTokens(file.CurrentContent) {
				if token.kind == 'i' {
					family.headUses[token.text] = struct{}{}
				}
			}
		}
		substitutions, _, ok := tokenSubstitutions(file)
		if !ok {
			continue
		}
		baseline, _ := declaredIdentifiers(file.BaselineContent, lang)
		current, _ := declaredIdentifiers(file.CurrentContent, lang)
		for previous, replacement := range substitutions {
			_, declaredAtBase := baseline[previous]
			_, survivesAtHead := current[previous]
			_, declaredAtHead := current[replacement]
			_, existedAtBase := baseline[replacement]
			if !declaredAtBase || survivesAtHead || !declaredAtHead || existedAtBase {
				continue
			}
			if family.localRenames[file.Path] == nil {
				family.localRenames[file.Path] = make(map[renamePair]struct{})
			}
			family.localRenames[file.Path][renamePair{previous: previous, current: replacement}] = struct{}{}
		}
	}
	return scope
}

func (s renameScope) family(name string) *familyScope {
	if existing, ok := s.families[name]; ok {
		return existing
	}
	created := &familyScope{
		localRenames: make(map[string]map[renamePair]struct{}),
		headUses:     make(map[string]struct{}),
	}
	s.families[name] = created
	return created
}

// declaresRename reports whether the file being judged performed, on itself,
// the exact declaration transition its substitution claims to be following.
//
// Only the file's own transitions count. Another file's transition is a claim
// about a name, and following it requires knowing that the reference resolves
// to that file rather than to any other declaration of the same name, which is
// the question no token stream can answer. See renameScope.
func (s renameScope) declaresRename(family, path, previous, current string) bool {
	scope, ok := s.families[family]
	if !ok {
		return false
	}
	_, local := scope.localRenames[path][renamePair{previous: previous, current: current}]
	return local
}

// oldNameSurvives reports whether the retired name still appears at head
// anywhere in this family's changed files.
func (s renameScope) oldNameSurvives(family, previous string) bool {
	scope, ok := s.families[family]
	if !ok {
		return false
	}
	_, survives := scope.headUses[previous]
	return survives
}

type sourceToken struct {
	kind byte
	text string
}

// mechanicallyEquivalent reports a change that renames symbols without
// changing what the code does.
//
// The rule is stated positively, and that direction is the whole point. An
// earlier version asked whether the substituted name collided with anything
// already in scope, and answered from the same-directory, same-extension
// siblings alone. Every symbol declared anywhere else was invisible, so
// swapping `requireAdmin` for `allowAnyone` across a package boundary read as
// a rename to a fresh name and routed an authorization weakening to the
// cheapest tier. Widening the collision set only moves that boundary; there is
// always another directory.
//
// So the evidence required is now the evidence a rename actually leaves. The
// substituted name must be DECLARED by the change set itself at head, because
// that is what renaming a symbol produces, and the replaced name must be gone
// from the change set's declarations, because a rename removes the old name
// rather than leaving both and picking the other one. A substitution to a
// symbol this change never declares is a different symbol, not a new name for
// the same one, and it scores as changed logic.
//
// The cost of the inversion is that a rename campaign whose declaration lives
// outside the diff scores as changed logic, which buys a review round. The
// cost of the previous direction was a missed authorization weakening at
// exit 0. Undecidable resolves toward changed logic.
//
// A NAME is not that evidence, and the round-3 review proved it: a flat set of
// names the change set declared at head was satisfiable by any second file
// renaming anything to the target name, and by a comment shaped like a
// declaration. A declaration TRANSITION carried by any file in the family was
// not enough either, and the round-4 review proved that: one throwaway file
// renaming its own dead helper vouched for a guard swap it has no relationship
// to. So the transition must be the judged file's OWN, the retired name has to
// have genuinely stopped appearing, and a high-risk path anywhere in the diff
// forfeits the route entirely. See renameScope.
func mechanicallyEquivalent(file FileChange, scope renameScope) bool {
	if file.Status == Renamed {
		return relocationPreservesCategory(file)
	}
	substitutions, changed, ok := tokenSubstitutions(file)
	if !ok {
		return false
	}
	family := sourceLanguage(file.Path).family
	for previous, replacement := range substitutions {
		if !scope.declaresRename(family, file.Path, previous, replacement) {
			return false
		}
		if scope.oldNameSurvives(family, previous) {
			return false
		}
	}
	return changed || file.BaselineContent != file.CurrentContent
}

// tokenSubstitutions reports the identifier substitutions that turn a file's
// baseline token stream into its head one, and whether the change is a pure
// substitution at all. It answers from the file alone, with no reference to the
// rest of the change set, so the same routine can both build the rename
// evidence and be judged against it without either step defining the other.
//
// ok is false whenever anything about the change is not a consistent identifier
// substitution: a differing token count, a keyword or literal moving, a
// substitution into a name already in baseline scope, a call target, a member
// of a declaration this file does not own, or a map that is not one-to-one.
func tokenSubstitutions(file FileChange) (map[string]string, bool, bool) {
	if file.Status != Modified || !relocationPreservesCategory(file) || !fileMatchesPath(file, sourcePath) || file.BaselineContent == "" || file.CurrentContent == "" || !sameBuildConstraints(file) {
		return nil, false, false
	}
	if file.BaselineContextTruncated {
		return nil, false, false
	}
	baseline := sourceTokens(file.BaselineContent)
	current := sourceTokens(file.CurrentContent)
	if len(baseline) != len(current) {
		return nil, false, false
	}
	baselineIdentifiers := make(map[string]struct{})
	for _, token := range sourceTokens(file.BaselineContent + "\n" + file.BaselineContext) {
		if token.kind == 'i' {
			baselineIdentifiers[token.text] = struct{}{}
		}
	}
	baselineEnclosing := enclosingBrackets(baseline)
	currentEnclosing := enclosingBrackets(current)
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
			return nil, false, false
		}
		if _, collision := baselineIdentifiers[right.text]; collision {
			return nil, false, false
		}
		if identifierControlsCallTarget(baseline, current, index) {
			return nil, false, false
		}
		if identifierNamesMemberOfAnotherDeclaration(baseline, baselineEnclosing, index) ||
			identifierNamesMemberOfAnotherDeclaration(current, currentEnclosing, index) {
			return nil, false, false
		}
		if mapped, ok := forward[left.text]; ok && mapped != right.text {
			return nil, false, false
		}
		if mapped, ok := reverse[right.text]; ok && mapped != left.text {
			return nil, false, false
		}
		forward[left.text] = right.text
		reverse[right.text] = left.text
	}
	return forward, changed, true
}

func relocationPreservesCategory(file FileChange) bool {
	if file.BaselinePath == "" {
		return true
	}
	if pathCategory(file.Path) != pathCategory(file.BaselinePath) || filepath.Ext(file.Path) != filepath.Ext(file.BaselinePath) {
		return false
	}
	if !sourcePath(file.Path) {
		return true
	}
	return false
}

func sameBuildConstraints(file FileChange) bool {
	if filepath.Ext(file.Path) != ".go" || filepath.Ext(file.BaselinePath) != ".go" && file.BaselinePath != "" {
		return true
	}
	baseline, baselineOK := buildConstraintSignature(file.BaselineContent)
	current, currentOK := buildConstraintSignature(file.CurrentContent)
	return baselineOK && currentOK && baseline == current
}

func buildConstraintSignature(content string) (string, bool) {
	var expressions []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if !constraint.IsGoBuild(line) && !constraint.IsPlusBuild(line) {
			continue
		}
		expression, err := constraint.Parse(line)
		if err != nil {
			return "", false
		}
		expressions = append(expressions, expression.String())
	}
	return strings.Join(expressions, "\n"), true
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
	// The declaration itself is the one place a name followed by a parameter
	// list is not a call target. Recognising only Go's `func` here made every
	// JavaScript, TypeScript, and Python function rename score as a call-target
	// change, which is stricter than intended and hid what the rule is for.
	return index == 0 || (!declarationKeyword(baseline[index-1].text) && !declarationKeyword(current[index-1].text))
}

// identifierNamesMemberOfAnotherDeclaration reports whether the identifier at
// index selects a member of a declaration this file does not own: a
// composite-literal or dictionary key, or a keyword argument at a call site.
// Those positions read as ordinary identifiers to the token comparison, so
// without this rule `Policy{RequireAdmin: true}` to `Policy{RequireAnyUser:
// true}` and `check(fail_closed=True)` to `check(fail_open=True)` both score as
// consistent renames and route an authorization change to the cheapest tier.
// The sibling-file collision check cannot see either one, because the field and
// the parameter are declared in another package.
//
// A declaration's own parameter list is excluded, so annotated parameters
// (`def guard(actor: str)`, `function f(strict: boolean)`) and default values
// stay renameable. Everything undecidable here resolves toward changed logic,
// which costs a review round rather than a missed weakening.
func identifierNamesMemberOfAnotherDeclaration(tokens []sourceToken, enclosing []int, index int) bool {
	if index+1 >= len(tokens) {
		return false
	}
	open := enclosing[index]
	if open < 0 {
		return false
	}
	switch tokens[index+1].text {
	case ":":
		// `:=` is a short variable declaration, not a key.
		if index+2 < len(tokens) && tokens[index+2].text == "=" {
			return false
		}
		if tokens[open].text == "{" {
			return true
		}
		return tokens[open].text == "(" && parenOpensCall(tokens, open)
	case "=":
		// `==` and every compound comparison read as two punctuation tokens.
		if index+2 < len(tokens) && tokens[index+2].text == "=" {
			return false
		}
		return tokens[open].text == "(" && parenOpensCall(tokens, open)
	default:
		return false
	}
}

// parenOpensCall reports whether the parenthesis at openIndex begins a call
// argument list rather than a declaration's parameter list.
func parenOpensCall(tokens []sourceToken, openIndex int) bool {
	if openIndex <= 0 || tokens[openIndex-1].kind != 'i' {
		return false
	}
	return openIndex < 2 || !declarationKeyword(tokens[openIndex-2].text)
}

func declarationKeyword(value string) bool {
	switch strings.ToLower(value) {
	case "func", "def", "fn", "function", "class", "sub", "proc":
		return true
	default:
		return false
	}
}

// declaringKeywords introduce a name the file itself owns. Import and export
// forms are deliberately absent: `import { allowAnyone }` binds a name in the
// file but declares nothing, and counting it would restore the exact hole the
// declaration rule exists to close, since swapping an import specifier is how
// a cross-directory guard substitution is spelled.
var declaringKeywords = map[string]struct{}{
	"func": {}, "def": {}, "fn": {}, "function": {}, "class": {}, "struct": {},
	"interface": {}, "type": {}, "enum": {}, "trait": {}, "impl": {}, "record": {},
	"var": {}, "let": {}, "const": {}, "sub": {}, "proc": {}, "typedef": {},
	"protocol": {}, "actor": {}, "object": {}, "union": {},
}

// declaredIdentifiers returns the names a source file declares itself.
//
// It reads the language's CODE token stream rather than parsing, so it stays
// cheap and multi-language, and it is deliberately incomplete: struct fields,
// plain assignments, and imported bindings are not declarations here. Missing a
// real declaration costs a review round; inventing one would hand back the
// mechanical-rename route, so every ambiguous position is left out.
//
// Commentary is removed before tokenizing and string literals are already
// single literal tokens, so nothing a human wrote as prose can be mistaken for
// a declaration. That is not a nicety: `// TODO: let allowAnyone be dropped`,
// sitting untouched in an unrelated file, was by itself enough evidence to make
// a cross-package authorization substitution score as a rename.
func declaredIdentifiers(content string, lang language) (map[string]struct{}, map[string]struct{}) {
	declared := make(map[string]struct{})
	referenceable := make(map[string]struct{})
	if content == "" {
		return declared, referenceable
	}
	tokens := codeTokens(content, lang)
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.kind == 'i' {
			if _, ok := declaringKeywords[strings.ToLower(token.text)]; ok {
				index = collectKeywordDeclaration(tokens, index, declared, referenceable)
			}
			continue
		}
		if token.kind == 'p' && token.text == ":" && index+1 < len(tokens) && tokens[index+1].text == "=" {
			collectShortVariableDeclaration(tokens, index, declared)
		}
	}
	return declared, referenceable
}

// declaredNames is the whole-set half of declaredIdentifiers, for callers that
// only need to know how many names a file introduces.
func declaredNames(content string, lang language) map[string]struct{} {
	all, _ := declaredIdentifiers(content, lang)
	return all
}

// collectKeywordDeclaration records the name a declaring keyword introduces,
// plus the parameter names of a function-shaped declaration, and returns the
// index the caller should continue from. Only the keyword's own name is added
// to the referenceable set.
func collectKeywordDeclaration(tokens []sourceToken, keyword int, declared, referenceable map[string]struct{}) int {
	cursor := keyword + 1
	// A Go method receiver sits between `func` and the name.
	if cursor < len(tokens) && tokens[cursor].text == "(" {
		cursor = afterMatching(tokens, cursor)
	}
	if cursor >= len(tokens) || tokens[cursor].kind != 'i' || sourceKeyword(tokens[cursor].text) {
		return keyword
	}
	declared[tokens[cursor].text] = struct{}{}
	// The keyword's own name is the only part of this another file could
	// reference. The parameter names collected below are scoped to this
	// declaration's body, and a short variable declaration is scoped tighter
	// still, so neither can be what a reference elsewhere resolves to.
	referenceable[tokens[cursor].text] = struct{}{}
	name := cursor
	cursor++
	if cursor < len(tokens) && tokens[cursor].text == "(" {
		collectParameterNames(tokens, cursor, declared)
		return afterMatching(tokens, cursor) - 1
	}
	return name
}

// collectParameterNames records the name position of each comma-separated
// group in a declaration's parameter list, which is the first identifier of
// the group in every layout this needs to handle: `name Type`, `name: Type`,
// and a bare `name`. Later identifiers in a group are the type, not a name.
func collectParameterNames(tokens []sourceToken, open int, declared map[string]struct{}) {
	depth := 0
	expectName := true
	for index := open; index < len(tokens); index++ {
		switch tokens[index].text {
		case "(", "[", "{":
			depth++
			continue
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return
			}
			continue
		case ",":
			if depth == 1 {
				expectName = true
			}
			continue
		}
		if depth != 1 || !expectName || tokens[index].kind != 'i' {
			continue
		}
		expectName = false
		if !sourceKeyword(tokens[index].text) {
			declared[tokens[index].text] = struct{}{}
		}
	}
}

// collectShortVariableDeclaration records every name in the comma-separated
// list to the left of a Go `:=`.
func collectShortVariableDeclaration(tokens []sourceToken, colon int, declared map[string]struct{}) {
	expectName := true
	for index := colon - 1; index >= 0; index-- {
		if expectName {
			if tokens[index].kind != 'i' || sourceKeyword(tokens[index].text) {
				return
			}
			declared[tokens[index].text] = struct{}{}
			expectName = false
			continue
		}
		if tokens[index].text != "," {
			return
		}
		expectName = true
	}
}

// afterMatching returns the index just past the bracket group opened at open.
func afterMatching(tokens []sourceToken, open int) int {
	depth := 0
	for index := open; index < len(tokens); index++ {
		switch tokens[index].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}
	return len(tokens)
}

// enclosingBrackets returns, for each token, the index of the innermost
// unclosed opening bracket, or -1 at the top level.
func enclosingBrackets(tokens []sourceToken) []int {
	enclosing := make([]int, len(tokens))
	var open []int
	for index, token := range tokens {
		if token.kind == 'p' {
			switch token.text {
			case ")", "]", "}":
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			}
		}
		if len(open) == 0 {
			enclosing[index] = -1
		} else {
			enclosing[index] = open[len(open)-1]
		}
		if token.kind == 'p' {
			switch token.text {
			case "(", "[", "{":
				open = append(open, index)
			}
		}
	}
	return enclosing
}

// sourceTokens reads every byte of the file, commentary included. It is the
// stream the baseline-to-head comparison runs over, because a comment edit is a
// content edit and has to stay visible to it.
func sourceTokens(content string) []sourceToken {
	return tokenize(content, commentSyntax{})
}

// codeTokens drops commentary before tokenizing. It is the stream declarations
// are collected from, because a comment is not a declaration.
func codeTokens(content string, lang language) []sourceToken {
	return tokenize(content, lang.comment)
}

func tokenize(content string, comments commentSyntax) []sourceToken {
	var tokens []sourceToken
	for index := 0; index < len(content); {
		if skipped := commentEnd(content, index, comments); skipped > index {
			index = skipped
			continue
		}
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

// nonSourceExtensions names the file kinds that carry no executable behavior.
// The set is the inverse of the old allowlist on purpose: an allowlist scored a
// language it had not heard of as not-source at all, so a diff in Zig, Elixir,
// Scala, or a shell script never reached the source branch of any axis. An
// unrecognised extension is now source, which costs a review round on a data
// format nobody listed and never silently drops a language off the map.
var nonSourceExtensions = map[string]struct{}{
	".md": {}, ".mdx": {}, ".markdown": {}, ".rst": {}, ".adoc": {}, ".txt": {},
	".json": {}, ".yaml": {}, ".yml": {}, ".toml": {}, ".ini": {}, ".cfg": {},
	".conf": {}, ".properties": {}, ".csv": {}, ".tsv": {}, ".lock": {},
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".svg": {}, ".ico": {},
	".webp": {}, ".pdf": {}, ".mp4": {}, ".mov": {}, ".webm": {}, ".tape": {},
	".gz": {}, ".zip": {}, ".tar": {}, ".bin": {}, ".log": {}, ".golden": {},
	".gitignore": {}, ".gitattributes": {}, ".gitmodules": {}, ".editorconfig": {},
	".dockerignore": {}, ".npmrc": {}, ".nvmrc": {}, ".prettierrc": {}, ".env": {},
}

// nonSourceBaseNames names the extensionless files that are documentation or
// metadata. Every other extensionless file stays source, because a file with a
// shebang and no extension is exactly the shape an allowlist misses.
var nonSourceBaseNames = map[string]struct{}{
	"license": {}, "licence": {}, "notice": {}, "authors": {}, "contributors": {},
	"codeowners": {}, "readme": {}, "changelog": {}, "owners": {}, "version": {},
}

func sourcePath(name string) bool {
	lower := strings.ToLower(filepath.ToSlash(name))
	ext := filepath.Ext(lower)
	if ext == "" {
		_, documentation := nonSourceBaseNames[filepath.Base(lower)]
		return !documentation
	}
	_, nonSource := nonSourceExtensions[ext]
	return !nonSource
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
	if agentInstructionPath(lower) || gateControlPath(lower) {
		return true
	}
	for _, pattern := range configured {
		if pathmatch.Match(lower, pattern) {
			return true
		}
	}
	return dependencyPath(lower)
}

// agentInstructionBaseNames are the files an agent reads as orders. In a
// repository whose contributors are AI, rewriting one of these is a privilege
// change: it is how you tell the next agent that tests are optional. They are
// built in rather than configured because the failure they close is a
// default-configuration repository waving its own instruction rewrite through,
// and a protection that only exists once an operator thinks to add it does not
// protect the operator who did not.
var agentInstructionBaseNames = map[string]struct{}{
	"agents.md": {}, "agent.md": {}, "claude.md": {}, "codex.md": {}, "gemini.md": {},
	"skill.md": {}, "cursorrules": {}, ".cursorrules": {}, ".windsurfrules": {},
	".clinerules": {}, "copilot-instructions.md": {}, "llms.txt": {}, "conventions.md": {},
}

// agentInstructionDirs are trees whose whole purpose is instructing an agent.
var agentInstructionDirs = []string{
	".claude/", ".codex/", ".cursor/", ".windsurf/", ".github/instructions/",
	".github/prompts/", "skills/", "prompts/",
}

func agentInstructionPath(lower string) bool {
	if _, ok := agentInstructionBaseNames[filepath.Base(lower)]; ok {
		return true
	}
	for _, dir := range agentInstructionDirs {
		if strings.Contains("/"+lower, "/"+dir) {
			return true
		}
	}
	return false
}

// gateControlPath names the committed files that decide how strictly this gate
// runs, or how git renders the diff every mechanical check reads. A change to
// one of them is a change to the gate, and the gate does not review changes to
// itself at the cheapest tier.
func gateControlPath(lower string) bool {
	switch filepath.Base(lower) {
	case ".no-slop.yaml", ".no-slop.yml", ".no-mistakes.yaml", ".no-mistakes.yml", ".gitattributes":
		return true
	default:
		return false
	}
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
	} else if d.Overridden {
		printed += fmt.Sprintf("\noverride raised: %s -> %s", d.OriginalTier, d.Tier)
	}
	printed += "\nprovenance: " + d.Rationale
	for _, escalation := range d.Escalations {
		printed += "\nescalation: " + escalation
	}
	if len(d.PriorityLenses) > 0 {
		printed += "\nlens priority: " + strings.Join(d.PriorityLenses, ", ")
	}
	if len(d.DeterministicProbes) > 0 {
		printed += "\ndeterministic probes: " + strings.Join(d.DeterministicProbes, ", ")
	}
	return printed
}
