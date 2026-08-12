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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gate takes no positional arguments")
	}

	workDir, err := filepath.Abs(*repo)
	if err != nil {
		return fmt.Errorf("resolve repository: %w", err)
	}
	repoCfg, err := config.LoadRepo(workDir)
	if err != nil {
		return err
	}
	globalCfg := config.DefaultGlobalConfig()
	cfg := config.Merge(globalCfg, repoCfg)
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
	changes, err := engine.LoadGitChanges(ctx, workDir, baseRef, headRef)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return fmt.Errorf("no committed changes between %s and %s", baseRef, headRef)
	}

	blocklistPath := strings.TrimSpace(*blocklist)
	blocklistConfigured := blocklistPath != ""
	if blocklistPath == "" {
		blocklistPath = cfg.Slop.LeakScan.BlocklistFile
		blocklistConfigured = strings.TrimSpace(repoCfg.Slop.LeakScan.BlocklistFile) != ""
	}
	blocklistEntries, blocklistState, err := loadBlocklist(workDir, blocklistPath, blocklistConfigured)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, blocklistState)
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
				fmt.Fprintf(stdout, "leak exemption: %s:%d: %s\n", exemption.Path, exemption.Line, exemption.Marker)
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
			}, selectedDecision, engine.Result{}, "error"))
			if recordErr != nil {
				return errors.Join(err, fmt.Errorf("record provenance: %w", recordErr))
			}
		}
		return err
	}
	outcome := "fail"
	if result.Passed {
		outcome = "pass"
	}
	printResult(stdout, result)
	if err := opts.ProvenanceStore.Append(provenanceRecord(provenanceInput{
		Provider:        *provider,
		Model:           *model,
		ReasoningEffort: *reasoningEffort,
		LaneID:          *laneID,
		ChangeClass:     resolvedChangeClass(*changeClass, changes),
		ChangeID:        baseRef + ".." + headRef,
	}, result.Decision, result, outcome)); err != nil {
		return fmt.Errorf("record provenance: %w", err)
	}
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
	return leakscan.ParseBlocklist(string(content)), fmt.Sprintf("leak scan: loaded %s private-name blocklist from %s", state, configured), nil
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

func printResult(stdout io.Writer, result engine.Result) {
	for _, check := range result.MandatoryChecks {
		if check.Enabled {
			fmt.Fprintf(stdout, "mandatory check: %s completed (%d findings)\n", check.Name, check.Findings)
		} else {
			fmt.Fprintf(stdout, "mandatory check: %s disabled\n", check.Name)
		}
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
		location := finding.Path
		if location != "" && finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		if location != "" {
			fmt.Fprintf(stdout, "finding: [%s] %s: %s\n", finding.Lens, location, finding.Description)
		} else {
			fmt.Fprintf(stdout, "finding: [%s] %s\n", finding.Lens, finding.Description)
		}
	}
	if count := len(result.LeakExemptions); count > 0 {
		label := "leak exemptions"
		if count == 1 {
			label = "leak exemption"
		}
		fmt.Fprintf(stdout, "leak scan: %d %s honored\n", count, label)
	}
	if result.Passed {
		fmt.Fprintln(stdout, "verdict: pass")
	} else {
		fmt.Fprintln(stdout, "verdict: fail")
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "NoSlop is the reviewer that knows the author is an AI.")
	fmt.Fprintln(output, "usage: noslop gate [--repo DIR] [--base REF] [--head REF] [--tier TIER] [--force-tier] [--thread URL] [--blocklist FILE] [--provider NAME] [--model NAME] [--reasoning-effort LEVEL] [--lane-id ID] [--change-class CLASS]")
	fmt.Fprintln(output, "       noslop evaluate --corpus DIR --unconditioned-results FILE --conditioned-results FILE")
}
