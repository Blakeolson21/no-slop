// Package cli owns the standalone noslop command surface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/slop/corpus"
	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/leakscan"
	"github.com/kunchenguid/no-mistakes/internal/slop/pathmatch"
	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
	"github.com/kunchenguid/no-mistakes/internal/slop/provenance"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

// Options supplies test seams without widening the command interface.
type Options struct {
	ReviewerFactory func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error)
	TestRunner      engine.TestRunner
	ThreadReader    prose.ThreadReader
	ProvenanceStore provenance.Store
}

// Run executes the noslop command and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		writeUsage(stdout)
		return 0
	}
	if args[0] == "evaluate" {
		if err := runEvaluate(args[1:], stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	if args[0] != "gate" {
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		writeUsage(stderr)
		return 2
	}
	if err := runGate(ctx, args[1:], stdout, stderr, opts); err != nil {
		var verdictErr *gateVerdictError
		if errors.As(err, &verdictErr) {
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func runEvaluate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusRoot := flags.String("corpus", "", "directory containing recorded corpus cases")
	caseSetPath := flags.String("case-set", "", "optional case-set manifest for a historical capture")
	unconditionedPath := flags.String("unconditioned-results", "", "captured unconditioned policy findings")
	conditionedPath := flags.String("conditioned-results", "", "captured conditioned policy findings")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("evaluate takes no positional arguments")
	}
	if strings.TrimSpace(*corpusRoot) == "" || strings.TrimSpace(*unconditionedPath) == "" || strings.TrimSpace(*conditionedPath) == "" {
		return fmt.Errorf("evaluate requires --corpus, --unconditioned-results, and --conditioned-results")
	}
	cases, err := corpus.Load(*corpusRoot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*caseSetPath) != "" {
		cases, err = corpus.LoadCaseSet(*caseSetPath, cases)
		if err != nil {
			return err
		}
	}
	unconditioned, err := corpus.LoadResults(*unconditionedPath)
	if err != nil {
		return fmt.Errorf("load unconditioned results: %w", err)
	}
	conditioned, err := corpus.LoadResults(*conditionedPath)
	if err != nil {
		return fmt.Errorf("load conditioned results: %w", err)
	}
	comparison, err := corpus.Compare(cases, unconditioned, conditioned)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "corpus: synthetic replay cases from %s\n", *corpusRoot)
	fmt.Fprintln(stdout, "results: replayed captures, not produced by this run")
	fmt.Fprintf(stdout, "unconditioned policy %q from %s\n", unconditioned.Policy, *unconditionedPath)
	fmt.Fprintf(stdout, "unconditioned: found %d, missed %d, false-positive %d\n", comparison.Unconditioned.Found, comparison.Unconditioned.Missed, comparison.Unconditioned.FalsePositive)
	fmt.Fprintf(stdout, "conditioned policy %q from %s\n", conditioned.Policy, *conditionedPath)
	fmt.Fprintf(stdout, "conditioned: found %d, missed %d, false-positive %d\n", comparison.Conditioned.Found, comparison.Conditioned.Missed, comparison.Conditioned.FalsePositive)
	fmt.Fprintf(stdout, "delta: found %+d, missed %+d, false-positive %+d\n",
		comparison.Conditioned.Found-comparison.Unconditioned.Found,
		comparison.Conditioned.Missed-comparison.Unconditioned.Missed,
		comparison.Conditioned.FalsePositive-comparison.Unconditioned.FalsePositive,
	)
	return nil
}

type gateVerdictError struct{}

func (*gateVerdictError) Error() string { return "gate failed" }

func runGate(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) error {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository worktree")
	base := flags.String("base", "", "base revision, default is merge-base with the default branch")
	head := flags.String("head", "HEAD", "head revision")
	tier := flags.String("tier", "auto", "validation tier: auto, leak-scan-only, single-review, full-adversarial")
	forceTier := flags.Bool("force-tier", false, "allow --tier to lower a provenance-escalated tier")
	thread := flags.String("thread", "", "GitHub issue or pull request URL for outbound text")
	blocklist := flags.String("blocklist", "", "private-name blocklist file override")
	provider := flags.String("provider", "", "generating agent provider")
	model := flags.String("model", "", "generating model")
	reasoningEffort := flags.String("reasoning-effort", "", "generating model reasoning effort")
	laneID := flags.String("lane-id", "", "generating agent lane identifier")
	changeClass := flags.String("change-class", "", "change class recorded with provenance")
	intent := flags.String("intent", "", "stated scope used by deterministic and reviewer checks")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gate takes no positional arguments")
	}

	threadRequested := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "thread" {
			threadRequested = true
		}
	})
	if threadRequested && strings.TrimSpace(*thread) == "" {
		return fmt.Errorf("--thread requires an issue or pull request URL")
	}

	workDir, err := filepath.Abs(*repo)
	if err != nil {
		return fmt.Errorf("resolve repository: %w", err)
	}
	headRepoCfg, err := config.LoadRepo(workDir)
	if err != nil {
		return err
	}
	globalCfg := config.DefaultGlobalConfig()
	branch, err := git.Run(ctx, workDir, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("resolve branch: %w", err)
	}
	defaultBranch := detectDefaultBranch(ctx, workDir)
	baseRef := strings.TrimSpace(*base)
	if baseRef == "" {
		baseRef, err = defaultBaseRef(ctx, workDir, defaultBranch, *head)
		if err != nil {
			return err
		}
	}
	headRef, err := git.Run(ctx, workDir, "rev-parse", *head)
	if err != nil {
		return fmt.Errorf("resolve head revision: %w", err)
	}
	baseRef, err = git.Run(ctx, workDir, "rev-parse", baseRef)
	if err != nil {
		return fmt.Errorf("resolve base revision: %w", err)
	}
	baseRepoCfg, err := loadBaseRepoConfig(ctx, workDir, baseRef)
	if err != nil {
		return err
	}
	// Every gate-strength value is resolved from the base ref, never from the
	// worktree. See the loadBaseRepoConfig comment for why.
	cfg := config.Merge(globalCfg, baseRepoCfg)
	drift := slopConfigDrift(config.Merge(globalCfg, headRepoCfg).Slop, cfg.Slop)

	changes, err := engine.LoadGitChanges(ctx, workDir, baseRef, headRef)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return fmt.Errorf("no committed changes between %s and %s", baseRef, headRef)
	}
	for _, warning := range unmatchedPatternWarnings(ctx, workDir, headRef, baseRepoCfg.Slop) {
		fmt.Fprintln(stdout, warning)
	}

	// The --blocklist flag adds names, it never replaces the configured list.
	// Replacing it would hand the audited party a way to point the identity
	// scan at an empty file from the command line, which is the same weakening
	// the base-ref resolution above closes for the committed config.
	blocklistPath := cfg.Slop.LeakScan.BlocklistFile
	blocklistConfigured := strings.TrimSpace(baseRepoCfg.Slop.LeakScan.BlocklistFile) != ""
	blocklistEntries, blocklistState, err := loadBlocklist(workDir, blocklistPath, blocklistConfigured)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, blocklistState)
	if override := strings.TrimSpace(*blocklist); override != "" {
		extraEntries, extraState, extraErr := loadBlocklist(workDir, override, true)
		if extraErr != nil {
			return extraErr
		}
		blocklistEntries = append(blocklistEntries, extraEntries...)
		fmt.Fprintln(stdout, extraState)
	}
	tierOverride := risk.Tier(*tier)
	if tierOverride == "auto" {
		tierOverride = ""
	}
	if opts.ProvenanceStore == nil {
		opts.ProvenanceStore = provenance.NewFileStore(resolveDataDir(workDir, cfg.Slop.DataDir))
	}
	input := engine.Input{
		WorkDir:       workDir,
		Branch:        branch,
		DefaultBranch: defaultBranch,
		BaseRef:       baseRef,
		HeadRef:       headRef,
		Intent:        strings.TrimSpace(*intent),
		Files:         changes,
		Config: engine.Config{
			Risk: risk.Config{
				SingleReviewThreshold: cfg.Slop.Risk.SingleReviewThreshold,
				FullReviewThreshold:   cfg.Slop.Risk.FullAdversarialThreshold,
				HighRiskPaths:         cfg.Slop.Risk.HighRiskPaths,
				ProvenanceStore:       opts.ProvenanceStore,
				AgentLaneID:           strings.TrimSpace(*laneID),
				Model:                 strings.TrimSpace(*model),
			},
			Blocklist:            blocklistEntries,
			RefuseLeakExemptions: !cfg.Slop.LeakScan.AllowExemptions,
			OutboundPaths:        cfg.Slop.Prose.OutboundPaths,
			AITellWords:          cfg.Slop.Prose.AITellWords,
			TestCountFloor:       cfg.Slop.TestCountFloor,
			TestCommand:          cfg.Slop.TestCommand,
			TierOverride:         tierOverride,
			ForceTier:            *forceTier,
			ThreadURL:            strings.TrimSpace(*thread),
			EvidenceRoot:         workDir,
			GateConfigDrift:      drift,
		},
	}

	if opts.TestRunner == nil {
		opts.TestRunner = engine.ShellTestRunner{}
	}
	if opts.ThreadReader == nil && input.Config.ThreadURL != "" {
		opts.ThreadReader = prose.NewGHThreadReader("")
	}
	if opts.ReviewerFactory == nil {
		opts.ReviewerFactory = defaultReviewerFactory
	}
	var reviewerCloser io.Closer
	var selectedDecision risk.Decision
	result, err := engine.Run(ctx, input, engine.Dependencies{
		ReviewerFactory: func(ctx context.Context) (engine.Reviewer, error) {
			reviewer, closer, createErr := opts.ReviewerFactory(ctx, cfg, stderr)
			reviewerCloser = closer
			return reviewer, createErr
		},
		Tests:        opts.TestRunner,
		ThreadReader: opts.ThreadReader,
		OnDecision: func(decision risk.Decision) {
			selectedDecision = decision
			fmt.Fprintln(stdout, decision.String())
		},
		OnLeakExemptions: func(exemptions []leakscan.Exemption) {
			for _, exemption := range exemptions {
				fmt.Fprintf(stdout, "leak exemption: %s:%d: %s (%d findings suppressed)\n", exemption.Path, exemption.Line, exemption.Marker, exemption.Suppressed)
			}
		},
	})
	if reviewerCloser != nil {
		defer reviewerCloser.Close()
	}
	if err != nil {
		if selectedDecision.Tier != "" {
			recordErr := opts.ProvenanceStore.Append(provenanceRecord(provenanceInput{
				Provider:        *provider,
				Model:           *model,
				ReasoningEffort: *reasoningEffort,
				LaneID:          *laneID,
				ChangeClass:     resolvedChangeClass(*changeClass, changes),
				ChangeID:        baseRef + ".." + headRef,
			}, selectedDecision, result, "error"))
			if recordErr != nil {
				printChecks(stdout, result)
				return errors.Join(err, fmt.Errorf("record provenance: %w", recordErr))
			}
		}
		// A refused run still reports what its mandatory checks found. Dropping
		// them made "the gate could not reach a reviewer" print the same as
		// "the gate looked and found nothing".
		printChecks(stdout, result)
		return err
	}
	outcome := "fail"
	if result.Passed {
		outcome = "pass"
	}
	// The provenance record is written before the verdict is printed. Printing
	// first meant a run whose append failed had already told stdout it passed
	// and then exited 2, so the two channels disagreed about the same run.
	if err := opts.ProvenanceStore.Append(provenanceRecord(provenanceInput{
		Provider:        *provider,
		Model:           *model,
		ReasoningEffort: *reasoningEffort,
		LaneID:          *laneID,
		ChangeClass:     resolvedChangeClass(*changeClass, changes),
		ChangeID:        baseRef + ".." + headRef,
	}, result.Decision, result, outcome)); err != nil {
		printChecks(stdout, result)
		return fmt.Errorf("record provenance: %w", err)
	}
	printResult(stdout, result)
	if !result.Passed {
		return &gateVerdictError{}
	}
	return nil
}

type provenanceInput struct {
	Provider        string
	Model           string
	ReasoningEffort string
	LaneID          string
	ChangeClass     string
	ChangeID        string
}

func provenanceRecord(input provenanceInput, decision risk.Decision, result engine.Result, outcome string) provenance.Record {
	byLens := make(map[string]provenance.LensFindings)
	for _, finding := range result.Findings {
		current := byLens[finding.Lens]
		current.Accepted = append(current.Accepted, provenance.Finding{
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
		if current.Rejected == nil {
			current.Rejected = []provenance.Finding{}
		}
		byLens[finding.Lens] = current
	}
	return provenance.Record{
		SchemaVersion:   provenance.CurrentSchemaVersion,
		ChangeID:        input.ChangeID,
		Provider:        input.Provider,
		Model:           input.Model,
		ReasoningEffort: input.ReasoningEffort,
		AgentLaneID:     input.LaneID,
		ChangeClass:     input.ChangeClass,
		SelectedTier:    string(decision.Tier),
		FindingsByLens:  byLens,
		Rounds:          result.ReviewRounds,
		FixGrowth:       0,
		Outcome:         outcome,
	}
}

// repoConfigFileName is the committed file every gate-strength value is read
// from.
const repoConfigFileName = ".no-mistakes.yaml"

// loadBaseRepoConfig reads the repository config from the base ref rather than
// from the worktree.
//
// A gate whose strictness is configured by the artifact being gated is not a
// gate. Reading .no-mistakes.yaml from the working tree meant the change under
// test chose its own review threshold, whether the test-count floor ran, which
// blocklist the identity scan loaded, whether inline leak exemptions were
// honored, and where provenance history lived. One commit could weaken an auth
// predicate, delete its test, and set test_count_floor: false, and the run
// printed an honest-looking axis block and passed. The uncommitted case was
// worse: a file the author never even committed did the same thing.
//
// The base ref is outside the blast radius of the change under test, and it is
// the same boundary the daemon already draws for the same fields (see the Repo
// Config Trust Boundary section of AGENTS.md, and EffectiveRepoConfig, which
// takes the whole slop block from the trusted default branch for exactly this
// reason). The whole block moves together rather than a hand-picked subset,
// because picking a subset is how the next field gets forgotten.
//
// Absent at base is valid and means built-in defaults. Present but unreadable
// or unparsable aborts the run: an undeterminable gate strength must never
// resolve to the permissive default.
func loadBaseRepoConfig(ctx context.Context, workDir, baseRef string) (*config.RepoConfig, error) {
	listing, err := git.Output(ctx, workDir, "ls-tree", "--name-only", "-z", baseRef, "--", repoConfigFileName)
	if err != nil {
		return nil, fmt.Errorf("read base repo config listing at %s: %w", baseRef, err)
	}
	if strings.TrimSpace(strings.ReplaceAll(listing, "\x00", "")) == "" {
		return &config.RepoConfig{}, nil
	}
	content, err := git.Output(ctx, workDir, "show", baseRef+":"+repoConfigFileName)
	if err != nil {
		return nil, fmt.Errorf("read base repo config at %s: %w", baseRef, err)
	}
	parsed, err := config.LoadRepoFromBytes([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("parse base repo config at %s: %w", baseRef, err)
	}
	return parsed, nil
}

// slopConfigDrift names every gate-strength field the head worktree sets
// differently from the base ref. The values are not honored, so the run has to
// say so: a contributor who tightened the gate deserves to know their change
// takes effect next run, and one who loosened it deserves to be named for it.
func slopConfigDrift(head, base config.Slop) []string {
	var drift []string
	compare := func(name string, headValue, baseValue any) {
		headText := fmt.Sprint(headValue)
		baseText := fmt.Sprint(baseValue)
		if headText != baseText {
			drift = append(drift, fmt.Sprintf("slop.%s is %s at head and %s at the base ref; the base value is the one in force", name, headText, baseText))
		}
	}
	compare("data_dir", head.DataDir, base.DataDir)
	compare("risk.single_review_threshold", head.Risk.SingleReviewThreshold, base.Risk.SingleReviewThreshold)
	compare("risk.full_adversarial_threshold", head.Risk.FullAdversarialThreshold, base.Risk.FullAdversarialThreshold)
	compare("risk.high_risk_paths", head.Risk.HighRiskPaths, base.Risk.HighRiskPaths)
	compare("leak_scan.blocklist_file", head.LeakScan.BlocklistFile, base.LeakScan.BlocklistFile)
	compare("leak_scan.allow_exemptions", head.LeakScan.AllowExemptions, base.LeakScan.AllowExemptions)
	compare("prose.outbound_paths", head.Prose.OutboundPaths, base.Prose.OutboundPaths)
	compare("prose.ai_tell_words", head.Prose.AITellWords, base.Prose.AITellWords)
	compare("test_count_floor", head.TestCountFloor, base.TestCountFloor)
	compare("test_command", head.TestCommand, base.TestCommand)
	return drift
}

// unmatchedPatternWarnings reports a configured glob that matches no path in
// the repository at head. An operator who writes a protection and gets silence
// has no way to tell a pattern that covers nothing from one that covers
// everything it should. It reads the RAW configured lists, so the built-in
// defaults never produce a warning about a directory the repository was never
// going to have.
func unmatchedPatternWarnings(ctx context.Context, workDir, headRef string, slop config.SlopRaw) []string {
	listing, err := git.Output(ctx, workDir, "ls-tree", "-r", "--name-only", "-z", headRef)
	if err != nil {
		return []string{fmt.Sprintf("config warning: head tree could not be listed, so configured patterns were not checked: %v", err)}
	}
	paths := strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00")
	var warnings []string
	for _, field := range []struct {
		name     string
		patterns []string
	}{
		{name: "slop.risk.high_risk_paths", patterns: slop.Risk.HighRiskPaths},
		{name: "slop.prose.outbound_paths", patterns: slop.Prose.OutboundPaths},
	} {
		for _, pattern := range field.patterns {
			matched := false
			for _, path := range paths {
				if path != "" && pathmatch.Match(path, pattern) {
					matched = true
					break
				}
			}
			if !matched {
				warnings = append(warnings, fmt.Sprintf("config warning: %s pattern %q matches no path at head", field.name, pattern))
			}
		}
	}
	return warnings
}

func resolveDataDir(workDir, configured string) string {
	configured = strings.TrimSpace(configured)
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(workDir, configured)
}

func resolvedChangeClass(configured string, changes []engine.Change) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	allMarkdown := true
	allTests := true
	for _, change := range changes {
		path := strings.ToLower(filepath.ToSlash(change.Path))
		extension := strings.ToLower(filepath.Ext(path))
		allMarkdown = allMarkdown && (extension == ".md" || extension == ".mdx")
		allTests = allTests && (strings.HasSuffix(path, "_test.go") || strings.Contains(path, ".test.") || strings.Contains(path, ".spec."))
	}
	switch {
	case allMarkdown:
		return "documentation"
	case allTests:
		return "tests"
	default:
		return "source-or-mixed"
	}
}

func defaultReviewerFactory(ctx context.Context, cfg *config.Config, stderr io.Writer) (engine.Reviewer, io.Closer, error) {
	if err := cfg.ResolveAgent(ctx, exec.LookPath); err != nil {
		return nil, nil, err
	}
	created := make([]agent.Agent, 0, len(cfg.Agents))
	for _, name := range cfg.Agents {
		current, err := agent.NewWithOptions(name, cfg.AgentPathFor(name), cfg.AgentArgsFor(name), agent.Options{
			ACPRegistryOverrides:   cfg.ACPRegistryOverrides,
			DisableProjectSettings: cfg.DisableProjectSettings,
		})
		if err != nil {
			for _, built := range created {
				_ = built.Close()
			}
			return nil, nil, err
		}
		created = append(created, current)
	}
	ag := agent.NewFallback(created)
	if ag == nil {
		return nil, nil, fmt.Errorf("no reviewer agent resolved")
	}
	if cfg.DisableProjectSettings {
		if err := agent.EnsureGateNeutralized(ag); err != nil {
			_ = ag.Close()
			return nil, nil, err
		}
	}
	return engine.NewAgentReviewer(ag, func(text string) { fmt.Fprint(stderr, text) }), ag, nil
}

func loadBlocklist(workDir, configured string, explicitlyConfigured bool) ([]string, string, error) {
	if configured == "" {
		return nil, "leak scan: no private-name blocklist configured", nil
	}
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicitlyConfigured {
			return nil, fmt.Sprintf("leak scan: no private-name blocklist (default path %s not present)", configured), nil
		}
		state := "default"
		if explicitlyConfigured {
			state = "configured"
		}
		return nil, "", fmt.Errorf("read private-name blocklist (%s path %s): %w", state, configured, err)
	}
	state := "default"
	if explicitlyConfigured {
		state = "configured"
	}
	// The entry count is printed because a readable file with no entries scans
	// exactly like a missing one, and an operator reading only "loaded" cannot
	// tell a working identity policy from an empty one.
	entries := leakscan.ParseBlocklist(string(content))
	return entries, fmt.Sprintf("leak scan: loaded %s private-name blocklist from %s (%d entries)", state, configured, len(entries)), nil
}

func detectDefaultBranch(ctx context.Context, workDir string) string {
	if remoteHead, err := git.Run(ctx, workDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(remoteHead, "origin/")
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := git.Run(ctx, workDir, "rev-parse", "--verify", candidate); err == nil {
			return candidate
		}
	}
	return "main"
}

func defaultBaseRef(ctx context.Context, workDir, defaultBranch, head string) (string, error) {
	for _, candidate := range []string{"origin/" + defaultBranch, defaultBranch} {
		if _, err := git.Run(ctx, workDir, "rev-parse", "--verify", candidate); err != nil {
			continue
		}
		base, err := git.Run(ctx, workDir, "merge-base", candidate, head)
		if err == nil {
			return base, nil
		}
	}
	return "", fmt.Errorf("resolve base revision: use --base to name it explicitly")
}

// printChecks prints every mandatory-check line and every finding gathered so
// far, without a verdict. A run that ended before it could reach one has no
// verdict to print, and printing one anyway is how stdout came to disagree
// with the exit code.
func printChecks(stdout io.Writer, result engine.Result) {
	for _, check := range result.MandatoryChecks {
		fmt.Fprintln(stdout, formatMandatoryCheck(check))
	}
	for _, finding := range result.Findings {
		fmt.Fprintln(stdout, formatFinding(finding))
	}
}

// formatMandatoryCheck names the detectors a check could not arm alongside its
// finding count, so "completed (0 findings)" never overstates the coverage the
// pass actually had.
func formatMandatoryCheck(check engine.MandatoryCheck) string {
	if !check.Enabled {
		return fmt.Sprintf("mandatory check: %s disabled", check.Name)
	}
	if len(check.Unarmed) == 0 {
		return fmt.Sprintf("mandatory check: %s completed (%d findings)", check.Name, check.Findings)
	}
	return fmt.Sprintf("mandatory check: %s completed (%d findings, not armed: %s)", check.Name, check.Findings, strings.Join(check.Unarmed, "; "))
}

func formatFinding(finding engine.Finding) string {
	location := finding.Path
	if location != "" && finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", location, finding.Line)
	}
	if location != "" {
		return fmt.Sprintf("finding: [%s] %s: %s", finding.Lens, location, finding.Description)
	}
	return fmt.Sprintf("finding: [%s] %s", finding.Lens, finding.Description)
}

func printResult(stdout io.Writer, result engine.Result) {
	for _, check := range result.MandatoryChecks {
		fmt.Fprintln(stdout, formatMandatoryCheck(check))
	}
	if result.ReviewRan {
		fmt.Fprintf(stdout, "review: completed (%d rounds)\n", result.ReviewRounds)
	} else {
		fmt.Fprintln(stdout, "review: skipped")
	}
	if len(result.Tests) > 0 {
		for _, test := range result.Tests {
			fmt.Fprintf(stdout, "tests: exit %d (%s)\n", test.ExitCode, test.Command)
		}
	} else {
		fmt.Fprintln(stdout, "tests: skipped")
	}
	for _, finding := range result.Findings {
		fmt.Fprintln(stdout, formatFinding(finding))
	}
	if count := len(result.LeakExemptions); count > 0 {
		label := "leak exemptions"
		if count == 1 {
			label = "leak exemption"
		}
		suppressed := 0
		for _, exemption := range result.LeakExemptions {
			suppressed += exemption.Suppressed
		}
		fmt.Fprintf(stdout, "leak scan: %d %s honored, %d findings suppressed\n", count, label, suppressed)
	}
	if result.Passed {
		fmt.Fprintln(stdout, "verdict: pass")
	} else {
		fmt.Fprintln(stdout, "verdict: fail")
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "NoSlop is the reviewer that knows the author is an AI.")
	fmt.Fprintln(output, "usage: noslop gate [--repo DIR] [--base REF] [--head REF] [--intent TEXT] [--tier TIER] [--force-tier] [--thread URL] [--blocklist FILE] [--provider NAME] [--model NAME] [--reasoning-effort LEVEL] [--lane-id ID] [--change-class CLASS]")
	fmt.Fprintln(output, "       noslop evaluate --corpus DIR [--case-set FILE] --unconditioned-results FILE --conditioned-results FILE")
}
