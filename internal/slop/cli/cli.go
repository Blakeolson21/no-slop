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
	"reflect"
	"strings"
	"unicode"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/identity"
	"github.com/Blakeolson21/no-slop/internal/slop/corpus"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/leakscan"
	"github.com/Blakeolson21/no-slop/internal/slop/pathmatch"
	"github.com/Blakeolson21/no-slop/internal/slop/prose"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
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
	base := flags.String("base", "", "base revision, which must be an ancestor of HEAD on the canonical ref")
	head := flags.String("head", "HEAD", "head revision")
	tier := flags.String("tier", "auto", "validation tier: auto, leak-scan-only, single-review, full-adversarial (may only raise the computed tier)")
	forceTier := flags.Bool("force-tier", false, "accepted for compatibility; it can no longer lower a computed tier")
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
	headRef, err := git.Run(ctx, workDir, "rev-parse", *head)
	if err != nil {
		return fmt.Errorf("resolve head revision: %w", err)
	}
	resolvedBase, err := resolveBase(ctx, workDir, headRef, strings.TrimSpace(*base))
	if err != nil {
		return err
	}
	// Which ref supplied the gate's strength is printed on every run. The
	// round-3 review passed an authorization weakening by naming a commit on
	// its own branch as the base, and nothing in the output said so.
	fmt.Fprintln(stdout, resolvedBase.String())
	baseRef := resolvedBase.Base
	defaultBranch := resolvedBase.CanonicalBranch
	baseRepoCfg := resolvedBase.Config
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
	dataDir := resolveDataDir(workDir, cfg.Slop.DataDir)
	if opts.ProvenanceStore == nil {
		opts.ProvenanceStore = provenance.NewFileStore(dataDir)
	}
	// The lane and model are strings the caller asserts about itself; nothing
	// authenticates them. That is a residual the product accepts, but a reviewer
	// reading the output has to be able to see the assertion for what it is,
	// which they could not when it printed only as part of a rationale sentence.
	// The configured path, not the resolved absolute one: NoSlop's own identity
	// scan treats a personal home path as a leak, so printing one in its own run
	// header would be the product contradicting itself on every run.
	fmt.Fprintln(stdout, provenanceIdentityLine(*laneID, *model, cfg.Slop.DataDir))
	if err := requireProvenanceStore(cfg.Slop.ProvenanceRequired, dataDir); err != nil {
		return err
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
// It reads both the canonical and legacy config names and resolves them
// through the same alias rules LoadRepo applies to the worktree, so the base
// ref is read exactly the way the head would have been.
func loadBaseRepoConfig(ctx context.Context, workDir, baseRef string) (*config.RepoConfig, error) {
	canonicalData, canonicalExists, err := readBaseRepoConfigFile(ctx, workDir, baseRef, identity.RepoConfigName)
	if err != nil {
		return nil, err
	}
	legacyData, legacyExists, err := readBaseRepoConfigFile(ctx, workDir, baseRef, identity.LegacyRepoConfigName)
	if err != nil {
		return nil, err
	}
	loaded, present, err := config.LoadRepoFromAliasBytes(canonicalData, canonicalExists, legacyData, legacyExists)
	if err != nil {
		return nil, fmt.Errorf("parse base repo config at %s: %w", baseRef, err)
	}
	if !present {
		return &config.RepoConfig{}, nil
	}
	return loaded, nil
}

func readBaseRepoConfigFile(ctx context.Context, workDir, baseRef, name string) ([]byte, bool, error) {
	listing, err := git.Output(ctx, workDir, "ls-tree", "--name-only", "-z", baseRef, "--", name)
	if err != nil {
		return nil, false, fmt.Errorf("read base repo config listing at %s: %w", baseRef, err)
	}
	if strings.TrimSpace(strings.ReplaceAll(listing, "\x00", "")) == "" {
		return nil, false, nil
	}
	content, err := git.Output(ctx, workDir, "show", baseRef+":"+name)
	if err != nil {
		return nil, false, fmt.Errorf("read base repo config at %s: %w", baseRef, err)
	}
	return []byte(content), true, nil
}

// slopConfigDrift names every gate-strength field the head worktree sets
// differently from the base ref. The values are not honored, so the run has to
// say so: a contributor who tightened the gate deserves to know their change
// takes effect next run, and one who loosened it deserves to be named for it.
//
// The field list was written out by hand, which is the shape that forgets the
// next field somebody adds. loadBaseRepoConfig's own comment says "picking a
// subset is how the next field gets forgotten", and this was the subset. It is
// now derived from config.Slop by reflection, with the yaml names taken from
// config.SlopRaw's tags, so a gate-strength field is compared from the moment
// it exists. TestSlopConfigDriftComparesEveryConfiguredField is the check that
// fails when the two structs stop mirroring one another.
func slopConfigDrift(head, base config.Slop) []string {
	return compareConfigFields("slop", reflect.ValueOf(head), reflect.ValueOf(base), reflect.TypeOf(config.SlopRaw{}))
}

func compareConfigFields(prefix string, head, base reflect.Value, rawType reflect.Type) []string {
	var drift []string
	for index := 0; index < head.NumField(); index++ {
		field := head.Type().Field(index)
		if !field.IsExported() {
			continue
		}
		name, rawField := configFieldName(rawType, field.Name)
		path := prefix + "." + name
		headValue := head.Field(index)
		baseValue := base.Field(index)
		if headValue.Kind() == reflect.Struct {
			nested := reflect.TypeOf(struct{}{})
			if rawField != nil {
				nested = derefType(rawField.Type)
			}
			drift = append(drift, compareConfigFields(path, headValue, baseValue, nested)...)
			continue
		}
		headText := fmt.Sprint(headValue.Interface())
		baseText := fmt.Sprint(baseValue.Interface())
		if headText != baseText {
			drift = append(drift, fmt.Sprintf("%s is %s at head and %s at the base ref; the base value is the one in force", path, headText, baseText))
		}
	}
	return drift
}

// configFieldName finds the yaml name the raw config uses for a resolved field.
// A resolved field with no raw counterpart still gets compared, under a name
// derived from its Go name: a broken mirror must never mean a silently
// uncompared gate control.
func configFieldName(rawType reflect.Type, goName string) (string, *reflect.StructField) {
	if rawType != nil && rawType.Kind() == reflect.Struct {
		if field, ok := rawType.FieldByName(goName); ok {
			if tag := strings.Split(field.Tag.Get("yaml"), ",")[0]; tag != "" && tag != "-" {
				return tag, &field
			}
			return snakeCase(goName), &field
		}
	}
	return snakeCase(goName), nil
}

func derefType(value reflect.Type) reflect.Type {
	for value.Kind() == reflect.Ptr {
		value = value.Elem()
	}
	return value
}

func snakeCase(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for index, current := range runes {
		upper := unicode.IsUpper(current)
		if upper && index > 0 {
			previous := runes[index-1]
			next := rune(0)
			if index+1 < len(runes) {
				next = runes[index+1]
			}
			if !unicode.IsUpper(previous) || (next != 0 && unicode.IsLower(next)) {
				out.WriteByte('_')
			}
		}
		out.WriteRune(unicode.ToLower(current))
	}
	return out.String()
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

// provenanceIdentityLine states, in the run header, exactly what the gate knows
// about who generated the change and where it looked for their history. Every
// part of it is self-asserted except the store path, which comes from the base
// ref, and the line says so.
func provenanceIdentityLine(laneID, model, dataDir string) string {
	lane := strings.TrimSpace(laneID)
	generator := strings.TrimSpace(model)
	if lane == "" {
		lane = "(none supplied)"
	}
	if generator == "" {
		generator = "(none supplied)"
	}
	return fmt.Sprintf("provenance identity: lane %s, model %s, self-asserted by the caller and not authenticated; history at %s", lane, generator, dataDir)
}

// requireProvenanceStore honors slop.provenance_required from the base ref. A
// repository that depends on escalation history can say that an absent store is
// not an acceptable answer, which is the operator-side half of the high-water
// mark the store itself keeps.
func requireProvenanceStore(required bool, dataDir string) error {
	if !required {
		return nil
	}
	path := filepath.Join(dataDir, provenance.FileName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("slop.provenance_required is set at the base ref but the provenance history at %s cannot be read: %w", path, err)
	}
	return nil
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

// ResolvedBase records which ref the gate took its strength from and how.
type ResolvedBase struct {
	// CanonicalRef is the ref the base must sit on, such as "origin/main".
	CanonicalRef string
	// CanonicalBranch is that ref's branch name, used for reversibility scoring.
	CanonicalBranch string
	// Base is the resolved base commit.
	Base string
	// Config is the repository config read at Base.
	Config *config.RepoConfig
	// Explicit says the caller supplied --base and it was verified.
	Explicit bool
	// Pinned says the canonical ref came from slop.base_ref at the base config
	// rather than from detection.
	Pinned bool
}

func (r ResolvedBase) String() string {
	origin := "merge-base with " + r.CanonicalRef
	if r.Explicit {
		origin = "--base, verified as an ancestor of HEAD on " + r.CanonicalRef
	}
	pinned := ""
	if r.Pinned {
		pinned = " (canonical ref pinned by slop.base_ref)"
	}
	return fmt.Sprintf("base: %s from %s%s", r.Base, origin, pinned)
}

// resolveBase decides which commit supplies the gate's strength.
//
// It used to be whatever `--base` said, which put the last control back inside
// the author's reach: committing a weakened .no-slop.yaml as the previous
// commit on the author's own branch and running `--base HEAD~1` made that file
// the operator's config, reported zero drift, and passed an authorization
// weakening at exit 0. The base ref being outside the blast radius of the
// change is the whole reason gate strength was moved there, and a caller-named
// commit is not outside it.
//
// So the base is the merge-base of HEAD with a CANONICAL ref: the remote and
// branch pinned by slop.base_ref in the repository config, or the detected
// default branch when nothing is pinned. An explicit --base is still accepted,
// because a pipeline legitimately knows the exact commit it branched from, but
// only when it is both an ancestor of HEAD and contained in the canonical ref,
// which is the pair of properties that makes it a commit the operator's history
// already approved. Anything else exits 2 rather than being quietly honored.
//
// The pin is read from the config at the provisionally resolved base, so an
// operator can move the canonical ref without the move being self-certifying:
// the ref that authorizes the change of ref is the previous canonical one.
func resolveBase(ctx context.Context, workDir, headRef, requested string) (ResolvedBase, error) {
	canonicalRef, canonicalBranch, err := detectCanonicalRef(ctx, workDir, config.SlopBaseRef{})
	if err != nil {
		// A repository whose trunk is not called main or master has to be able
		// to say so, and the config that says so cannot be read until a base
		// exists. The head tree's copy names a candidate ref, and nothing else:
		// the base resolved through it must itself carry the same pin, so the
		// name has to already be established in the history it points at rather
		// than asserted by the change under test.
		bootstrapped, bootstrapErr := bootstrapPinnedBase(ctx, workDir, headRef)
		if bootstrapErr != nil {
			return ResolvedBase{}, errors.Join(err, bootstrapErr)
		}
		return applyRequestedBase(ctx, workDir, headRef, requested, bootstrapped)
	}
	provisional, err := mergeBase(ctx, workDir, canonicalRef, headRef)
	if err != nil {
		return ResolvedBase{}, err
	}
	provisionalCfg, err := loadBaseRepoConfig(ctx, workDir, provisional)
	if err != nil {
		return ResolvedBase{}, err
	}
	pinned := false
	if pin := config.Merge(config.DefaultGlobalConfig(), provisionalCfg).Slop.BaseRef; pin.Remote != "" || pin.Branch != "" {
		pinnedRef, pinnedBranch, pinErr := detectCanonicalRef(ctx, workDir, pin)
		if pinErr != nil {
			return ResolvedBase{}, pinErr
		}
		if pinnedRef != canonicalRef {
			canonicalRef, canonicalBranch, pinned = pinnedRef, pinnedBranch, true
			provisional, err = mergeBase(ctx, workDir, canonicalRef, headRef)
			if err != nil {
				return ResolvedBase{}, err
			}
			provisionalCfg, err = loadBaseRepoConfig(ctx, workDir, provisional)
			if err != nil {
				return ResolvedBase{}, err
			}
		}
	}

	return applyRequestedBase(ctx, workDir, headRef, requested, ResolvedBase{
		CanonicalRef:    canonicalRef,
		CanonicalBranch: canonicalBranch,
		Base:            provisional,
		Config:          provisionalCfg,
		Pinned:          pinned,
	})
}

// applyRequestedBase verifies an explicit --base against the canonical ref the
// caller had no say in, and leaves the resolved base alone when none was given.
func applyRequestedBase(ctx context.Context, workDir, headRef, requested string, resolved ResolvedBase) (ResolvedBase, error) {
	if requested == "" {
		return resolved, nil
	}
	explicit, err := git.Run(ctx, workDir, "rev-parse", requested+"^{commit}")
	if err != nil {
		return ResolvedBase{}, fmt.Errorf("resolve base revision %q: %w", requested, err)
	}
	if !isAncestor(ctx, workDir, explicit, headRef) {
		return ResolvedBase{}, fmt.Errorf("--base %s is not an ancestor of the head revision; the gate's strength is read from the base ref, so it may only be a commit the head already contains", requested)
	}
	if !isAncestor(ctx, workDir, explicit, resolved.CanonicalRef) {
		return ResolvedBase{}, fmt.Errorf("--base %s is not contained in %s; the gate's strength is read from the base ref, so it may only be a commit the canonical ref already carries, not one on the branch under test", requested, resolved.CanonicalRef)
	}
	explicitCfg, err := loadBaseRepoConfig(ctx, workDir, explicit)
	if err != nil {
		return ResolvedBase{}, err
	}
	resolved.Base = explicit
	resolved.Config = explicitCfg
	resolved.Explicit = true
	return resolved, nil
}

// bootstrapPinnedBase is the one route by which the head tree gets a say in
// which ref is canonical, and it is deliberately a dead end for an author.
//
// A repository whose trunk is not main or master has to be able to name it, and
// the config that names it cannot be read before a base exists. So the head
// tree's copy supplies a CANDIDATE ref, the base is resolved through it, and
// the config at that base must independently carry the same pin. An author
// pointing this at their own branch gets a base on their own branch whose
// config either does not carry the pin, in which case the run refuses, or does,
// in which case they committed the pin to the history the pin names, which is
// the same bar as planting anything else in the operator's history.
func bootstrapPinnedBase(ctx context.Context, workDir, headRef string) (ResolvedBase, error) {
	headCfg, err := loadBaseRepoConfig(ctx, workDir, headRef)
	if err != nil {
		return ResolvedBase{}, err
	}
	pin := config.Merge(config.DefaultGlobalConfig(), headCfg).Slop.BaseRef
	if pin.Remote == "" && pin.Branch == "" {
		return ResolvedBase{}, fmt.Errorf("no conventional default branch and no slop.base_ref at head to name one")
	}
	canonicalRef, canonicalBranch, err := detectCanonicalRef(ctx, workDir, pin)
	if err != nil {
		return ResolvedBase{}, err
	}
	base, err := mergeBase(ctx, workDir, canonicalRef, headRef)
	if err != nil {
		return ResolvedBase{}, err
	}
	baseCfg, err := loadBaseRepoConfig(ctx, workDir, base)
	if err != nil {
		return ResolvedBase{}, err
	}
	confirmed := config.Merge(config.DefaultGlobalConfig(), baseCfg).Slop.BaseRef
	if confirmed != pin {
		return ResolvedBase{}, fmt.Errorf("slop.base_ref at head names %q but the config at %s does not, so the head is naming its own canonical ref; commit the pin to that ref's history first", canonicalRef, base)
	}
	return ResolvedBase{
		CanonicalRef:    canonicalRef,
		CanonicalBranch: canonicalBranch,
		Base:            base,
		Config:          baseCfg,
		Pinned:          true,
	}, nil
}

// detectCanonicalRef names the ref the base has to sit on. A pinned remote or
// branch wins; otherwise the conventional default branch names, remote copy
// first. Failing to find one aborts the run: a gate that cannot tell which
// history is the operator's cannot tell whose config it is reading.
//
// `refs/remotes/origin/HEAD` is deliberately NOT consulted, and that is not a
// simplification. It is an ordinary local symbolic ref: `git remote set-head`
// writes it, and a clone made by fetching one branch records that branch in it.
// Dogfooding this fix caught exactly that, with the gate happily naming
// `origin/feature/slop-engine-v1` as the canonical ref for a change on
// `feature/slop-engine-v1`, which is the whole defect S3 exists to close
// wearing a different hat. A name the author of the change can set is not a
// statement about which history is the operator's.
func detectCanonicalRef(ctx context.Context, workDir string, pin config.SlopBaseRef) (string, string, error) {
	remote := strings.TrimSpace(pin.Remote)
	branch := strings.TrimSpace(pin.Branch)
	if branch != "" {
		candidates := []string{branch}
		if remote != "" {
			candidates = []string{remote + "/" + branch, branch}
		}
		for _, candidate := range candidates {
			if _, err := git.Run(ctx, workDir, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
				return candidate, branch, nil
			}
		}
		return "", "", fmt.Errorf("resolve canonical base ref: slop.base_ref names %q, which this repository does not have", strings.Join(candidates, " or "))
	}

	prefix := "origin"
	if remote != "" {
		prefix = remote
	}
	for _, name := range []string{"main", "master"} {
		for _, candidate := range []string{prefix + "/" + name, name} {
			if _, err := git.Run(ctx, workDir, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
				return candidate, name, nil
			}
		}
	}
	return "", "", fmt.Errorf("resolve canonical base ref: no %s/main, %s/master, main, or master in this repository; set slop.base_ref in the repository config to name the ref the gate should read its strength from", prefix, prefix)
}

func mergeBase(ctx context.Context, workDir, canonicalRef, headRef string) (string, error) {
	base, err := git.Run(ctx, workDir, "merge-base", canonicalRef, headRef)
	if err != nil {
		return "", fmt.Errorf("resolve base revision: %s and the head revision share no history: %w", canonicalRef, err)
	}
	return strings.TrimSpace(base), nil
}

func isAncestor(ctx context.Context, workDir, candidate, descendant string) bool {
	_, err := git.Run(ctx, workDir, "merge-base", "--is-ancestor", candidate, descendant)
	return err == nil
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

// formatMandatoryCheck names the detectors a check could not arm and the ones
// that ran on reduced coverage, alongside its finding count, so "completed
// (0 findings)" never overstates the coverage the pass actually had.
func formatMandatoryCheck(check engine.MandatoryCheck) string {
	if !check.Enabled {
		return fmt.Sprintf("mandatory check: %s disabled", check.Name)
	}
	qualifiers := make([]string, 0, 2)
	if len(check.Unarmed) > 0 {
		qualifiers = append(qualifiers, "not armed: "+strings.Join(check.Unarmed, "; "))
	}
	if len(check.Degraded) > 0 {
		qualifiers = append(qualifiers, "reduced coverage: "+strings.Join(check.Degraded, "; "))
	}
	if len(qualifiers) == 0 {
		return fmt.Sprintf("mandatory check: %s completed (%d findings)", check.Name, check.Findings)
	}
	return fmt.Sprintf("mandatory check: %s completed (%d findings, %s)", check.Name, check.Findings, strings.Join(qualifiers, ", "))
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
