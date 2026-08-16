// Package engine runs NoSlop's risk-proportional front-stage pipeline.
package engine

import (
	"context"
	"fmt"

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
	// ScanBinaryNotScanned means the head blob is binary and text patterns
	// were not run against it.
	ScanBinaryNotScanned ScanState = "binary-not-scanned"
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
	// Unreadable is set when this path's content could not be loaded at all.
	// The entry is quarantined rather than aborting the run.
	Unreadable string
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
	// GateConfigDrift names each gate-strength field the head worktree sets
	// differently from the base ref. The base value is the one in force; the
	// drift becomes a finding so that a change editing the gate's own controls
	// is itself flagged rather than silently ignored.
	GateConfigDrift []string
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

// Finding is one named gate failure.
type Finding struct {
	Lens        string
	Severity    string
	Path        string
	Line        int
	Description string
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

	driftFindings := make([]Finding, 0, len(input.Config.GateConfigDrift))
	for _, drift := range input.Config.GateConfigDrift {
		driftFindings = append(driftFindings, Finding{
			Lens:        "gate-config-drift",
			Severity:    "error",
			Path:        ".no-mistakes.yaml",
			Description: drift,
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

	leakFindings, exemptions := runLeakScan(input.Files, input.Config.Blocklist, input.Config.RefuseLeakExemptions)
	result.Findings = append(result.Findings, leakFindings...)
	result.LeakExemptions = exemptions
	if deps.OnLeakExemptions != nil {
		deps.OnLeakExemptions(exemptions)
	}
	result.MandatoryChecks = append(result.MandatoryChecks, MandatoryCheck{Name: "leak scan", Enabled: true, Findings: len(leakFindings)})
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

	result.Passed = len(result.Findings) == 0
	return result, nil
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

func runLeakScan(files []Change, blocklist []string, refuseExemptions bool) ([]Finding, []leakscan.Exemption) {
	input := make([]leakscan.File, 0, len(files))
	var result []Finding
	for _, file := range files {
		if file.Unreadable != "" {
			continue
		}
		if file.ScanState == ScanBinaryNotScanned {
			result = append(result, Finding{
				Lens:        "leak-scan-undetermined",
				Severity:    "error",
				Path:        file.Path,
				Description: "content is binary at head, so the leak scan could not read it",
			})
			continue
		}
		input = append(input, leakscan.File{Path: file.Path, Content: file.AddedContent})
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
	return result, scan.Exemptions
}

// runContentIntegrity turns every path this run could not read into its own
// finding. Quarantining the entry keeps one absent submodule object, or one
// unreadable blob, from aborting a gate that could still have scanned every
// other path in the change.
func runContentIntegrity(files []Change) []Finding {
	var findings []Finding
	for _, file := range files {
		if file.Unreadable != "" {
			findings = append(findings, Finding{
				Lens:        "content-unreadable",
				Severity:    "error",
				Path:        file.Path,
				Description: file.Unreadable,
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
