// Package engine runs NoSlop's risk-proportional front-stage pipeline.
package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/identity"
	"github.com/Blakeolson21/no-slop/internal/slop/leakscan"
	"github.com/Blakeolson21/no-slop/internal/slop/lenses"
	"github.com/Blakeolson21/no-slop/internal/slop/precheck"
	"github.com/Blakeolson21/no-slop/internal/slop/prose"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
	"github.com/Blakeolson21/no-slop/internal/slop/testfloor"
)

// ScanState records how much of a path the mandatory scans could actually see.
type ScanState string

const (
	// ScanFromDiff is the ordinary case: added content came from diff hunks.
	ScanFromDiff ScanState = ""
	// ScanWholeBlobFallback means git produced no hunks for a text file whose
	// content changed, so the whole head blob was scanned instead.
	ScanWholeBlobFallback ScanState = "whole-blob-fallback"
	// ScanBinarySafe means the head blob is binary, so the leak scan read it
	// through the binary-safe renderings rather than as diff text. The blob is
	// still scanned: naming it as skipped was honest and still let one NUL byte
	// carry a live credential past a mandatory check at exit 0.
	ScanBinarySafe ScanState = "binary-safe"
	// ScanSubmodulePointer means the entry is a gitlink. Its content lives in
	// another repository and this run never saw it.
	ScanSubmodulePointer ScanState = "submodule-pointer"
)

// Change contains the revision content each mechanical check needs.
type Change struct {
	Path            string
	BaselinePath    string
	Status          risk.ChangeStatus
	Added           int
	Deleted         int
	AddedContent    string
	CurrentContent  string
	BaselineContent string
	BaselineContext string
	// BaselineContextTruncated says the sibling scope stopped short of the
	// full set, so no collision answer derived from it is sound.
	BaselineContextTruncated bool
	// ScanState says how the mandatory scans saw this path.
	ScanState ScanState
	// SubmodulePointer carries the commits a gitlink entry moved between. It
	// is set only when ScanState is ScanSubmodulePointer.
	SubmodulePointer SubmodulePointer
	// Unreadable is set when this path's content could not be loaded at all.
	// The entry is quarantined rather than aborting the run.
	Unreadable string
}

// SubmodulePointer names the commits a submodule entry moved between.
type SubmodulePointer struct {
	BaselineCommit string
	HeadCommit     string
}

// Config controls classification and mandatory artifact checks.
type Config struct {
	Risk                 risk.Config
	Blocklist            []string
	RefuseLeakExemptions bool
	OutboundPaths        []string
	AITellWords          []string
	TestCountFloor       bool
	TestCommand          string
	TierOverride         risk.Tier
	ForceTier            bool
	ThreadURL            string
	EvidenceRoot         string
	// GateConfigDrift names each gate-strength input the head worktree sets
	// differently from the base ref. The base value is the one in force; the
	// drift becomes a finding so that a change editing the gate's own controls
	// is itself flagged rather than silently ignored.
	GateConfigDrift []ConfigDrift
	// BaseUnverified carries the reason the canonical base could not be
	// verified against the operator's remote. A run that could not establish
	// which history is the operator's cannot certify anything against it, so it
	// is pinned to the full tier, reads built-in defaults rather than a base
	// config it cannot trust, and fails.
	BaseUnverified string
}

// ConfigDrift is one gate-strength input the head sets differently from the
// base ref, and the file a reviewer should open to see it.
//
// It carries its own path because the drift set stopped being one file. The
// repo config was the only member until round 5 found that the private-name
// blocklist's CONTENT was read from the head worktree while only its PATH came
// from the base, so emptying a tracked, operator-configured list disarmed the
// identity scan with no drift reported anywhere. Naming .no-slop.yaml as the
// location of that finding would have sent the reviewer to the wrong file.
//
// Detail is the whole sentence, built by the caller that found the drift,
// because what a contributor should do about it differs by input: a slop.*
// value lands on the base branch in its own commit, and a blocklist entry
// lands there too but is not a yaml key anybody can grep for.
type ConfigDrift struct {
	Path   string
	Detail string
}

// Input is the complete gate request.
type Input struct {
	WorkDir       string
	Branch        string
	DefaultBranch string
	BaseRef       string
	HeadRef       string
	Intent        string
	Files         []Change
	Config        Config
}

// Severity says whether a finding blocks the verdict.
const (
	// SeverityError is a finding the run fails on.
	SeverityError = "error"
	// SeverityNotice is a finding the run reports and does not fail on,
	// because the answer it needs is a human's rather than the gate's.
	//
	// There is exactly one of these and adding a second needs the same
	// argument: a submodule pointer bump moves content that lives in another
	// repository, so no scan this gate can run will ever see it. Failing every
	// such change unconditionally made the gate unusable in any repository that
	// has a submodule and left no route but turning the gate off, which is a
	// worse outcome than the one it was defending. The bump is named, the leak
	// scan says it went unscanned, and the tier is RAISED so a reviewer looks at
	// it. What is removed is only the automatic failure, not the visibility.
	SeverityNotice = "notice"
)

// ErrBaseUnverified is the refusal a run makes when the canonical base could
// not be established. It is a refusal rather than a failing verdict because a
// gate that cannot say which commit it is judging against cannot certify
// anything, so it declines to render a verdict instead of rendering one it did
// not earn.
var ErrBaseUnverified = errors.New("the canonical base could not be verified, so this run certifies nothing")

// Finding is one named gate result. Severity decides whether it blocks.
type Finding struct {
	Lens        string
	Severity    string
	Path        string
	Line        int
	Description string
}

// Blocks reports whether this finding fails the run. An unset severity blocks,
// so a finding added without thinking about severity fails closed.
func (f Finding) Blocks() bool {
	return f.Severity != SeverityNotice
}

// ReviewRequest is the narrow reviewer seam.
type ReviewRequest struct {
	WorkDir       string
	Branch        string
	BaseRef       string
	HeadRef       string
	Intent        string
	Tier          risk.Tier
	Round         ReviewRound
	PriorFindings []Finding
	Prompt        string
}

// ReviewRound identifies one pass in a tier's reviewer protocol.
type ReviewRound string

const (
	ReviewRoundLensReview           ReviewRound = "lens-review"
	ReviewRoundAdversarialChallenge ReviewRound = "adversarial-challenge"
)

// Reviewer runs the AI-authorship lens pass.
type Reviewer interface {
	Review(context.Context, ReviewRequest) ([]Finding, error)
}

// TestResult is the configured command's captured result.
type TestResult struct {
	Command  string
	ExitCode int
	Output   string
}

// MandatoryCheck records whether a tier-independent check ran and how many
// findings it produced.
type MandatoryCheck struct {
	Name     string
	Enabled  bool
	Findings int
	// Unarmed names the parts of this check that could not run. A check that
	// reports zero findings without saying which of its detectors never fired
	// claims more coverage than it had.
	Unarmed []string
	// Degraded names the parts of this check that ran with reduced coverage.
	// It is deliberately separate from Unarmed: "did not look" and "looked
	// through a narrower window" are different claims, and reporting the second
	// as the first is what made a scanned binary blob read as a skipped one.
	Degraded []string
	// Widened names the parts of this check that had to be read more than one
	// way to be read at all.
	//
	// It is the third distinct claim and not a variant of Degraded, which says
	// coverage was LOST. A file carrying a zero-width space inside a credential
	// was scanned as written and again with the invisible characters removed, so
	// coverage was gained rather than lost, and filing that under reduced
	// coverage would tell a reviewer to distrust the one result they should
	// trust most. It is reported at all because "completed (0 findings)" over a
	// file whose bytes render two different ways is a sentence a reviewer should
	// be able to see the shape of.
	Widened []string
}

// TestRunner executes the configured full-tier test command.
type TestRunner interface {
	Run(context.Context, string, string) (TestResult, error)
}

// Dependencies are adapters at the engine's external seams.
type Dependencies struct {
	Reviewer         Reviewer
	ReviewerFactory  func(context.Context) (Reviewer, error)
	Tests            TestRunner
	ThreadReader     prose.ThreadReader
	OnDecision       func(risk.Decision)
	OnLeakExemptions func([]leakscan.Exemption)
}

// Result contains the visible route and every named finding.
type Result struct {
	Decision        risk.Decision
	Findings        []Finding
	LeakExemptions  []leakscan.Exemption
	MandatoryChecks []MandatoryCheck
	Tests           []TestResult
	ReviewRan       bool
	ReviewRounds    int
	Passed          bool
}

// Run classifies first, runs mandatory checks, then spends only the work the
// selected tier requires.
func Run(ctx context.Context, input Input, deps Dependencies) (Result, error) {
	riskFiles := make([]risk.FileChange, 0, len(input.Files))
	for _, file := range input.Files {
		riskFiles = append(riskFiles, risk.FileChange{
			Path:                     file.Path,
			BaselinePath:             file.BaselinePath,
			Status:                   file.Status,
			Added:                    file.Added,
			Deleted:                  file.Deleted,
			BaselineContent:          file.BaselineContent,
			BaselineContext:          file.BaselineContext,
			BaselineContextTruncated: file.BaselineContextTruncated,
			CurrentContent:           file.CurrentContent,
		})
	}
	riskConfig := input.Config.Risk
	riskConfig.OverrideTier = input.Config.TierOverride
	riskConfig.ForceTier = input.Config.ForceTier
	riskConfig.BaseUnverified = input.Config.BaseUnverified != ""
	riskConfig.UnscannableContent = unscannableContentPaths(input.Files)
	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        input.Branch,
		DefaultBranch: input.DefaultBranch,
		Files:         riskFiles,
	}, riskConfig)
	if err != nil {
		if decision.Tier != "" && deps.OnDecision != nil {
			deps.OnDecision(decision)
		}
		return Result{}, err
	}
	if deps.OnDecision != nil {
		deps.OnDecision(decision)
	}
	result := Result{Decision: decision}

	if input.Config.BaseUnverified != "" {
		result.Findings = append(result.Findings, Finding{
			Lens:     "base-ref-unverified",
			Severity: SeverityError,
			Description: fmt.Sprintf(
				"%s; the gate reads its strength from the operator's history, so with that history unestablished it certifies nothing and every gate-strength value came from built-in defaults",
				input.Config.BaseUnverified),
		})
	}

	driftFindings := make([]Finding, 0, len(input.Config.GateConfigDrift))
	for _, drift := range input.Config.GateConfigDrift {
		path := drift.Path
		if path == "" {
			path = identity.RepoConfigName
		}
		driftFindings = append(driftFindings, Finding{
			Lens:        "gate-config-drift",
			Severity:    "error",
			Path:        path,
			Description: drift.Detail,
		})
	}
	result.Findings = append(result.Findings, driftFindings...)
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{Name: "gate config", Enabled: true, Findings: len(driftFindings)})

	integrityFindings := runContentIntegrity(input.Files)
	result.Findings = append(result.Findings, integrityFindings...)
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{Name: "content integrity", Enabled: true, Findings: len(integrityFindings)})

	lensFindings, unarmed := runLensPrecheck(input.Files, input.Intent)
	result.Findings = append(result.Findings, lensFindings...)
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{
		Name:     "lens pre-check",
		Enabled:  true,
		Findings: len(lensFindings),
		Unarmed:  unarmed,
	})

	leakFindings, exemptions, unscanned, degraded, widened := runLeakScan(input.Files, input.Config.Blocklist, input.Config.RefuseLeakExemptions)
	result.Findings = append(result.Findings, leakFindings...)
	result.LeakExemptions = exemptions
	if deps.OnLeakExemptions != nil {
		deps.OnLeakExemptions(exemptions)
	}
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{
		Name:     "leak scan",
		Enabled:  true,
		Findings: len(leakFindings),
		Unarmed:  unscanned,
		Degraded: degraded,
		Widened:  widened,
	})
	testFloorEnabled := input.Config.TestCountFloor || containsProbe(decision.DeterministicProbes, "test-count-floor")
	testFloorFindings := []Finding(nil)
	if testFloorEnabled {
		testFloorFindings = runTestFloor(input.Files)
		result.Findings = append(result.Findings, testFloorFindings...)
	}
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{Name: "test-count floor", Enabled: testFloorEnabled, Findings: len(testFloorFindings)})
	proseFindings, err := runProseOracle(ctx, input, deps.ThreadReader)
	if err != nil {
		return result, err
	}
	result.Findings = append(result.Findings, proseFindings...)
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{Name: "prose oracle", Enabled: true, Findings: len(proseFindings)})
	// The live-thread check gets its own line whether or not it ran. A verified
	// open thread previously produced output byte-identical to never having
	// looked, so an operator could not tell "the thread is fine" from "no check
	// happened".
	threadFindings := 0
	for _, finding := range proseFindings {
		if finding.Lens == string(prose.ThreadClosed) || finding.Lens == string(prose.DuplicateClaim) {
			threadFindings++
		}
	}
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{
		Name:     "live thread check",
		Enabled:  input.Config.ThreadURL != "",
		Findings: threadFindings,
	})

	// An unverified base has already decided this run. The blocking
	// base-ref-unverified finding above is unconditional, so nothing the
	// reviewer or the test command could report can change the outcome, and the
	// tier it was pinned to is the most expensive one there is: two LLM
	// invocations and the configured suite, spent on a result fixed before the
	// first round starts. Since the only production caller never supplies a
	// pipeline base, that bill was charged to every offline run, every run
	// against a remote that will not answer, and every run whose object store
	// lacks the commit ls-remote named.
	//
	// This returns ErrBaseUnverified rather than a failing verdict on purpose.
	// A gate that could not establish which commit it is judging against has no
	// authority to render a verdict at all, so the honest answer is the same
	// refusal it already gave when it could not reach a reviewer: the mandatory
	// checks and every finding still print, and the caller still exits 2. The
	// pin, the reported tier, and the refusal are unchanged; what is removed is
	// the cost, plus a finding that actively misled, because the unverified
	// route reads built-in defaults and built-in defaults carry no test command,
	// so the run used to blame a missing slop.test_command for a base it could
	// not verify.
	if input.Config.BaseUnverified != "" {
		return result, fmt.Errorf("%w: %s", ErrBaseUnverified, input.Config.BaseUnverified)
	}

	if decision.Tier == risk.TierSingleReview || decision.Tier == risk.TierFullAdversarial {
		reviewer := deps.Reviewer
		if reviewer == nil && deps.ReviewerFactory != nil {
			reviewer, err = deps.ReviewerFactory(ctx)
			if err != nil {
				// The mandatory checks already ran. Returning them with the
				// error keeps a run that could not reach a reviewer from
				// throwing away the findings it had already earned, which is
				// what made a refused run indistinguishable from a run that
				// found nothing.
				return result, fmt.Errorf("create slop reviewer: %w", err)
			}
		}
		if reviewer == nil {
			result.Findings = append(result.Findings, Finding{
				Lens:        "reviewer-unavailable",
				Severity:    "error",
				Description: "selected tier requires a reviewer, but none is configured",
			})
		} else {
			rounds := []ReviewRound{ReviewRoundLensReview}
			if decision.Tier == risk.TierFullAdversarial {
				rounds = append(rounds, ReviewRoundAdversarialChallenge)
			}
			var priorFindings []Finding
			for _, round := range rounds {
				findings, reviewErr := reviewer.Review(ctx, ReviewRequest{
					WorkDir:       input.WorkDir,
					Branch:        input.Branch,
					BaseRef:       input.BaseRef,
					HeadRef:       input.HeadRef,
					Intent:        input.Intent,
					Tier:          decision.Tier,
					Round:         round,
					PriorFindings: append([]Finding(nil), priorFindings...),
					Prompt:        lenses.ReviewerPromptWithPriority(decision.PriorityLenses),
				})
				if reviewErr != nil {
					return result, fmt.Errorf("run slop reviewer round %q: %w", round, reviewErr)
				}
				result.ReviewRan = true
				result.ReviewRounds++
				result.Findings = appendUniqueFindings(result.Findings, findings...)
				priorFindings = append(priorFindings, findings...)
			}
		}
	}

	if decision.Tier == risk.TierFullAdversarial {
		switch {
		case input.Config.TestCommand == "":
			result.Findings = append(result.Findings, Finding{
				Lens:        "tests-not-configured",
				Severity:    "error",
				Description: "full-adversarial tier requires slop.test_command",
			})
		case deps.Tests == nil:
			result.Findings = append(result.Findings, Finding{
				Lens:        "test-runner-unavailable",
				Severity:    "error",
				Description: "full-adversarial tier requires a test runner",
			})
		default:
			testResult, testErr := deps.Tests.Run(ctx, input.WorkDir, input.Config.TestCommand)
			if testErr != nil {
				return result, fmt.Errorf("run full-tier tests: %w", testErr)
			}
			result.Tests = append(result.Tests, testResult)
			if testResult.ExitCode != 0 {
				result.Findings = append(result.Findings, Finding{
					Lens:        "test-failure",
					Severity:    "error",
					Description: fmt.Sprintf("configured tests failed with exit code %d", testResult.ExitCode),
				})
			}
		}
	}

	result.Passed = true
	for _, finding := range result.Findings {
		if finding.Blocks() {
			result.Passed = false
			break
		}
	}
	return result, nil
}

// unscannableContentPaths names every path in the change whose content this
// gate structurally cannot read. It feeds the classifier rather than the
// verdict: the answer to "something changed here that no scan of mine will ever
// see" is a review, not a refusal.
func unscannableContentPaths(files []Change) []string {
	var paths []string
	for _, file := range files {
		if file.ScanState == ScanSubmodulePointer {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

func runLensPrecheck(files []Change, intent string) ([]Finding, []string) {
	input := make([]precheck.File, 0, len(files))
	for _, file := range files {
		input = append(input, precheck.File{
			Path:            file.Path,
			BaselinePath:    file.BaselinePath,
			AddedContent:    file.AddedContent,
			BaselineContent: file.BaselineContent,
			CurrentContent:  file.CurrentContent,
		})
	}
	scan := precheck.Scan(input, intent)
	result := make([]Finding, 0, len(scan.Findings))
	for _, finding := range scan.Findings {
		result = append(result, Finding{
			Lens:        finding.Lens,
			Severity:    "error",
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return result, scan.Unarmed
}

func containsProbe(probes []string, target string) bool {
	for _, probe := range probes {
		if probe == target {
			return true
		}
	}
	return false
}

func appendUniqueFindings(existing []Finding, additions ...Finding) []Finding {
	for _, addition := range additions {
		duplicate := false
		for _, current := range existing {
			if addition.Lens == current.Lens && addition.Path == current.Path && addition.Line == current.Line {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, addition)
		}
	}
	return existing
}

// runLeakScan returns findings, honored exemptions, the paths the scan could
// not be run against at all, and the paths it read through a narrower window.
//
// Whether a path is read as text or through the binary-safe renderings is
// decided HERE, from the head blob's own bytes, and never from how git chose to
// render the diff. Two rounds of this defect had the same root: a binary blob
// was first skipped outright, then scanned but only when git's own hunk output
// was missing. Git samples the first 8000 bytes, so one NUL past that offset
// made git render an ordinary text hunk, the fallback returned early, the
// regex failed across the NUL, and a live AWS key reached "completed (0
// findings)" at exit 0 with nothing outside the committed diff. An uncommitted
// `.git/info/attributes` line did the same from the other side. Reading the
// bytes ourselves closes both with one condition, and leaves git attributes,
// `.git/info/attributes`, and diff rendering with no influence at all over
// whether leak scanning happens.
func runLeakScan(files []Change, blocklist []string, refuseExemptions bool) ([]Finding, []leakscan.Exemption, []string, []string, []string) {
	input := make([]leakscan.File, 0, len(files))
	var result []Finding
	var unscanned []string
	var degraded []string
	var widened []string
	for _, file := range files {
		if file.Unreadable != "" {
			continue
		}
		if file.ScanState == ScanSubmodulePointer {
			unscanned = append(unscanned, fmt.Sprintf("%s is a submodule pointer whose content is outside this repository", file.Path))
			continue
		}
		// The invisible-character question is asked of the head blob, and the
		// answer decides what the scanner is handed, so the claim on the
		// mandatory-check line is always about the bytes that were scanned. It
		// used to be asked of the head blob and answered over the added lines: a
		// path carrying a byte order mark at line 1, or a zero-width space in a
		// line the change never touched, printed "read more than one way" while
		// the scanner saw only added lines that had no invisible character in
		// them and therefore exactly one rendering. A check line asserting
		// coverage the run did not have is the same defect as a passing verdict
		// over a base the run could not verify.
		invisible := leakscan.HasInvisibleRunes(file.CurrentContent)
		if leakscan.IsBinaryContent(file.CurrentContent) {
			degraded = append(degraded, fmt.Sprintf("%s is binary at head and was read through the binary-safe renderings", file.Path))
			if invisible {
				widened = append(widened, fmt.Sprintf("%s carries Unicode format or control characters, so its head blob was read both as written and with them removed", file.Path))
			}
			input = append(input, leakscan.File{Path: file.Path, Content: file.CurrentContent, Binary: true})
			continue
		}
		// A text blob whose diff git would not render is scanned whole rather
		// than by hunk. That is a different window from every other path, so it
		// has to reach the mandatory-check line: the state was recorded on the
		// change and read by nothing, which left the run output byte-identical
		// to a path scanned normally while AGENTS.md claimed the fallback "says
		// so". A check that saw a path differently must say which path.
		if file.ScanState == ScanWholeBlobFallback {
			degraded = append(degraded, fmt.Sprintf("%s produced no diff hunks, so the whole head blob was scanned instead of the added lines", file.Path))
		}
		content := file.AddedContent
		if invisible {
			widened = append(widened, fmt.Sprintf("%s carries Unicode format or control characters, so its whole head blob was read both as written and with them removed", file.Path))
			content = file.CurrentContent
		}
		input = append(input, leakscan.File{Path: file.Path, Content: content})
	}
	scan := leakscan.Scan(input, leakscan.Options{Blocklist: blocklist, RefuseExemptions: refuseExemptions})
	for _, finding := range scan.Findings {
		result = append(result, Finding{
			Lens:        "leak-identity-scan",
			Severity:    "error",
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return result, scan.Exemptions, unscanned, degraded, widened
}

// runContentIntegrity turns every path whose content this run could not see
// into its own finding. Quarantining the entry keeps one unreadable blob, or
// one submodule, from aborting a gate that could still have scanned every other
// path in the change.
//
// A path this repository holds but could not read is a broken repository, and
// it blocks. A submodule pointer bump is not: the content genuinely lives in
// another repository and no scan available here will ever see it, so failing
// every such change was a permanent refusal with no route through it, in a
// shape an operator could only answer by not running the gate. It is reported
// as a notice naming the submodule and both commits, the leak scan says the
// path went unscanned, and the tier is raised so the change is reviewed rather
// than waved past. Reading a gitlink as a blob instead produced a git internal
// error on every healthy submodule bump, which is a broken gate rather than a
// judgement about the change.
func runContentIntegrity(files []Change) []Finding {
	var findings []Finding
	for _, file := range files {
		switch {
		case file.Unreadable != "":
			findings = append(findings, Finding{
				Lens:        "content-unreadable",
				Severity:    SeverityError,
				Path:        file.Path,
				Description: file.Unreadable,
			})
		case file.ScanState == ScanSubmodulePointer:
			findings = append(findings, Finding{
				Lens:     "submodule-pointer-unscanned",
				Severity: SeverityNotice,
				Path:     file.Path,
				Description: fmt.Sprintf(
					"submodule %q moved from %s to %s; its content is in another repository and was not scanned by this run, so the tier is raised for review",
					file.Path, file.SubmodulePointer.BaselineCommit, file.SubmodulePointer.HeadCommit),
			})
		}
	}
	return findings
}

func runTestFloor(files []Change) []Finding {
	baseline := make([]testfloor.File, 0, len(files))
	current := make([]testfloor.File, 0, len(files))
	for _, file := range files {
		baselinePath := file.BaselinePath
		if baselinePath == "" {
			baselinePath = file.Path
		}
		baseline = append(baseline, testfloor.File{Path: baselinePath, Content: file.BaselineContent})
		current = append(current, testfloor.File{Path: file.Path, Content: file.CurrentContent})
	}
	floor := testfloor.Compare(baseline, current)
	if floor.Passed {
		return nil
	}
	return []Finding{{
		Lens:        "test-capitulation",
		Severity:    "error",
		Description: fmt.Sprintf("test-count floor dropped from %d to %d", floor.Baseline, floor.Current),
	}}
}

func runProseOracle(ctx context.Context, input Input, threadReader prose.ThreadReader) ([]Finding, error) {
	artifacts := make([]prose.Artifact, 0, len(input.Files))
	for _, file := range input.Files {
		artifacts = append(artifacts, prose.Artifact{Path: file.Path, Content: file.CurrentContent})
	}
	findings, err := prose.Check(ctx, artifacts, prose.Options{
		OutboundPaths: input.Config.OutboundPaths,
		AITellWords:   input.Config.AITellWords,
		ThreadURL:     input.Config.ThreadURL,
		ThreadReader:  threadReader,
		EvidenceRoot:  input.Config.EvidenceRoot,
	})
	if err != nil {
		return nil, err
	}
	result := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		result = append(result, Finding{
			Lens:        string(finding.Kind),
			Severity:    "error",
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return result, nil
}
