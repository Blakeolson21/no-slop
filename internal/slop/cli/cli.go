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
	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/leakscan"
	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

// Options supplies test seams without widening the command interface.
type Options struct {
	ReviewerFactory func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error)
	TestRunner      engine.TestRunner
	ThreadReader    prose.ThreadReader
}

// Run executes the noslop command and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		writeUsage(stdout)
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

type gateVerdictError struct{}

func (*gateVerdictError) Error() string { return "gate failed" }

func runGate(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) error {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository worktree")
	base := flags.String("base", "", "base revision, default is merge-base with the default branch")
	head := flags.String("head", "HEAD", "head revision")
	tier := flags.String("tier", "auto", "validation tier: auto, leak-scan-only, single-review, full-adversarial")
	thread := flags.String("thread", "", "GitHub issue or pull request URL for outbound text")
	blocklist := flags.String("blocklist", "", "private-name blocklist file override")
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

	explicitBlocklist := strings.TrimSpace(*blocklist) != ""
	blocklistPath := strings.TrimSpace(*blocklist)
	if blocklistPath == "" {
		blocklistPath = cfg.Slop.LeakScan.BlocklistFile
	}
	blocklistEntries, err := loadBlocklist(workDir, blocklistPath, explicitBlocklist)
	if err != nil {
		return err
	}
	tierOverride := risk.Tier(*tier)
	if tierOverride == "auto" {
		tierOverride = ""
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
			},
			Blocklist:      blocklistEntries,
			OutboundPaths:  cfg.Slop.Prose.OutboundPaths,
			AITellWords:    cfg.Slop.Prose.AITellWords,
			TestCountFloor: cfg.Slop.TestCountFloor,
			TestCommand:    cfg.Slop.TestCommand,
			TierOverride:   tierOverride,
			ThreadURL:      strings.TrimSpace(*thread),
			EvidenceRoot:   workDir,
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
	result, err := engine.Run(ctx, input, engine.Dependencies{
		ReviewerFactory: func(ctx context.Context) (engine.Reviewer, error) {
			reviewer, closer, createErr := opts.ReviewerFactory(ctx, cfg, stderr)
			reviewerCloser = closer
			return reviewer, createErr
		},
		Tests:        opts.TestRunner,
		ThreadReader: opts.ThreadReader,
		OnDecision: func(decision risk.Decision) {
			fmt.Fprintln(stdout, decision.String())
		},
	})
	if reviewerCloser != nil {
		defer reviewerCloser.Close()
	}
	if err != nil {
		return err
	}
	printResult(stdout, result)
	if !result.Passed {
		return &gateVerdictError{}
	}
	return nil
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

func loadBlocklist(workDir, configured string, required bool) ([]string, error) {
	if configured == "" {
		return nil, nil
	}
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil, nil
		}
		return nil, fmt.Errorf("read private-name blocklist: %w", err)
	}
	return leakscan.ParseBlocklist(string(content)), nil
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
	if result.Passed {
		fmt.Fprintln(stdout, "verdict: pass")
	} else {
		fmt.Fprintln(stdout, "verdict: fail")
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "NoSlop is the reviewer that knows the author is an AI.")
	fmt.Fprintln(output, "usage: noslop gate [--repo DIR] [--base REF] [--head REF] [--tier TIER] [--thread URL] [--blocklist FILE]")
}
