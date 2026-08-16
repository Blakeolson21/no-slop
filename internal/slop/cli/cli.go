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

// PipelineBase is a base commit an orchestrating pipeline computed for this
// run, supplied through the Go API rather than through the command line.
//
// This is the one channel that can name the base directly, and it exists
// because a pipeline that carved the worktree, resolved the trunk, and knows
// the exact commit the branch left it at is not the audited party. Reaching it
// requires being in-process with the gate: `noslop gate` never sets it, no flag
// reaches it, and no file or ref does either. That property is the whole point
// and it is why the `--base` flag was removed rather than validated. A flag is
// something the author of the change under test writes.
type PipelineBase struct {
	// Commit is the exact base revision the pipeline resolved.
	Commit string
	// Origin says how the pipeline resolved it, for the run header.
	Origin string
	// DefaultBranch is the trunk the pipeline branched from. The classifier
	// scores reversibility differently on the default branch, and with no
	// canonical ref detection running there is nothing else to read it from.
	DefaultBranch string
}

// Options supplies the Go-API seams the command line deliberately does not
// expose.
type Options struct {
	ReviewerFactory func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error)
	TestRunner      engine.TestRunner
	ThreadReader    prose.ThreadReader
	ProvenanceStore provenance.Store
	// PipelineBase is the orchestrator-computed base. See PipelineBase.
	PipelineBase *PipelineBase
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
	// --base is accepted only so that passing it produces an explanation
	// instead of "flag provided but not defined". It cannot set the base. See
	// resolveBase: a flag is written by the author of the change under test,
	// and validating one against a canonical ref is the fix shape that failed
	// in rounds 3 and 4.
	base := flags.String("base", "", "removed; the base comes from the pipeline or from the remote, never from the command line")
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
	baseRequested := false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "thread":
			threadRequested = true
		case "base":
			baseRequested = true
		}
	})
	if baseRequested || strings.TrimSpace(*base) != "" {
		return fmt.Errorf("--base was removed and no longer selects the base revision. Gate strength is read from the base ref, so a base the caller names is a gate the caller configures: the canonical commit now comes from `git ls-remote` against the configured remote, or from an orchestrating pipeline through the Go API, and from nothing else. Set slop.base_ref at the canonical ref to point the gate at a different remote or branch")
	}
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
	resolvedBase, err := resolveBase(ctx, workDir, headRef, opts.PipelineBase)
	if err != nil {
		return err
	}
	// Which commit supplied the gate's strength, and how the run established it
	// is the operator's, is printed on every run. Round 3 passed an
	// authorization weakening by naming a commit on its own branch, and round 4
	// did it again through a local ref wearing the canonical ref's name, both
	// times with a run header nothing could distinguish from an honest one.
	fmt.Fprintln(stdout, resolvedBase.String())
	baseRef := resolvedBase.Base
	defaultBranch := resolvedBase.CanonicalBranch
	baseRepoCfg := resolvedBase.Config
	// Every gate-strength value is resolved from the base ref, never from the
	// worktree. See the loadBaseRepoConfig comment for why.
	cfg := config.Merge(globalCfg, baseRepoCfg)
	// Drift compares the head's gate controls against the base's. With an
	// unverified base there is no operator config to compare against, so the
	// comparison would report every configured value as drift and say nothing
	// true. The base-ref-unverified finding is the report in that case.
	var drift []string
	if resolvedBase.Verified() {
		drift = slopConfigDrift(config.Merge(globalCfg, headRepoCfg).Slop, cfg.Slop)
	}

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
			BaseUnverified:       resolvedBase.Unverified,
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

// ResolvedBase records which commit supplied the gate's strength, and how the
// run established that the commit is the operator's rather than the author's.
type ResolvedBase struct {
	// CanonicalRef names the ref the base sits on, in the form the run header
	// prints, such as "origin/main".
	CanonicalRef string
	// CanonicalBranch is that ref's branch name, used for reversibility
	// scoring. It is left empty on an unverified base: a local ref does not get
	// to tell the classifier which branch is the trunk.
	CanonicalBranch string
	// CanonicalCommit is the commit the remote reported for the canonical ref
	// when this run asked it.
	CanonicalCommit string
	// Base is the resolved base commit.
	Base string
	// Config is the repository config read at Base. It is deliberately empty on
	// an unverified base: a config read from a commit whose provenance this run
	// could not establish is not the operator's config.
	Config *config.RepoConfig
	// Pinned says the canonical ref came from slop.base_ref rather than from
	// the conventional trunk names.
	Pinned bool
	// Pipeline says an orchestrating pipeline supplied the base directly.
	Pipeline bool
	// PipelineOrigin is that pipeline's own description of how it resolved it.
	PipelineOrigin string
	// Unverified carries the reason the canonical commit could not be
	// established from the operator's remote. Empty means verified.
	Unverified string
}

// Verified reports whether the base came from a source the author of the change
// under test cannot write.
func (r ResolvedBase) Verified() bool { return r.Unverified == "" }

// String is the run header's base line. Each of the three routes reads
// differently on purpose. The previous header printed "base: <sha> from
// merge-base with origin/main" whether origin/main was the operator's remote
// branch or a local branch the author had created ten seconds earlier under
// that name, which made a defeated run byte-identical to an honest one.
func (r ResolvedBase) String() string {
	switch {
	case r.Pipeline:
		return fmt.Sprintf("base: %s supplied by the orchestrating pipeline (%s); no local ref and no flag took part", r.Base, r.PipelineOrigin)
	case !r.Verified():
		return fmt.Sprintf("base: %s from merge-base with the LOCAL ref %s, UNVERIFIED: %s. Gate strength is pinned to full-adversarial and every slop.* value came from built-in defaults rather than from that commit", r.Base, r.CanonicalRef, r.Unverified)
	}
	pinned := ""
	if r.Pinned {
		pinned = ", pinned by slop.base_ref"
	}
	return fmt.Sprintf("base: %s from merge-base with %s at %s, verified by ls-remote against the configured remote%s", r.Base, r.CanonicalRef, r.CanonicalCommit, pinned)
}

// resolveBase decides which commit supplies the gate's strength.
//
// Three rounds of review have taken the same control away from the caller in
// three different disguises, and the shape of the answer is now the shape of
// what is left after all of them.
//
// Round 3 removed `--base` as given: naming a commit on the author's own branch
// made the author's own weakened config the operator's config. The replacement
// was merge-base with a canonical ref, and `--base` survived as a request
// validated against that ref.
//
// Round 4 defeated the canonical ref itself. It was resolved by asking git to
// rev-parse the string "origin/main", and `git rev-parse` searches refs/,
// refs/tags/, refs/heads/, refs/remotes/ in order, so `git branch origin/main
// <own-commit>`, `git tag origin/main <own-commit>`, `git update-ref
// refs/remotes/origin/main <own-commit>`, and `git fetch . +<sha>:refs/remotes/
// origin/main` each made an author-owned commit the base. Every one of those is
// a single command, and remote-tracking refs are ordinary local refs: nothing
// in a clone's object store is a statement about which history the operator
// keeps.
//
// The lesson the round-4 reviewer drew is the rule this function follows: the
// fixes that held REMOVED a capability, and the fixes that fell CONSTRAINED an
// input the author can write. So there is no author-writable input here at all.
// The resolution order is exactly three sources, and local ref state is not one
// of them:
//
//  1. An orchestrating pipeline that supplies the base through the Go API. It
//     carved the worktree and resolved the trunk itself, and reaching this
//     channel requires being in-process with the gate.
//  2. The network. `git ls-remote <configured remote URL> refs/heads/<branch>`
//     is asked what the canonical branch points at, and the merge-base is taken
//     against the commit the remote answered with. No local ref participates,
//     under any name.
//  3. Nothing. Offline, or with no remote, or with a remote that will not
//     answer, the run is pinned to the full tier, reads built-in defaults
//     instead of a base config it cannot trust, and fails with the reason
//     named. It never lowers, which is the S1 move: the cheap route is removed
//     rather than made conditional.
//
// `--base` is gone rather than validated, because validating it is the fix
// shape that failed twice.
//
// The residual, stated plainly: the remote's URL comes from `.git/config`, which
// is local. An author who repoints origin at a repository they control can still
// make that repository's answer the canonical one. That is a materially louder
// act than creating a ref, it is one an operator can see, and closing it needs
// the URL pinned somewhere the change cannot rewrite. slop.base_ref.remote picks
// which remote is asked, and it is read from the base config, so an operator who
// wants that decision outside the worktree already has it.
func resolveBase(ctx context.Context, workDir, headRef string, pipeline *PipelineBase) (ResolvedBase, error) {
	if pipeline != nil {
		return resolvePipelineBase(ctx, workDir, *pipeline)
	}
	resolved, verifyErr := resolveVerifiedBase(ctx, workDir, headRef)
	if verifyErr == nil {
		return resolved, nil
	}
	return unverifiedBase(ctx, workDir, headRef, verifyErr)
}

// resolvePipelineBase trusts the orchestrator's commit and only checks that it
// names one. There is nothing further to verify: the pipeline is not the
// audited party, and a pipeline that resolved the wrong commit has a bug rather
// than a motive.
func resolvePipelineBase(ctx context.Context, workDir string, pipeline PipelineBase) (ResolvedBase, error) {
	requested := strings.TrimSpace(pipeline.Commit)
	if requested == "" {
		return ResolvedBase{}, fmt.Errorf("resolve base revision: the orchestrating pipeline supplied an empty base commit")
	}
	resolved, err := git.Run(ctx, workDir, "rev-parse", "--verify", requested+"^{commit}")
	if err != nil {
		return ResolvedBase{}, fmt.Errorf("resolve pipeline base %q: %w", requested, err)
	}
	base := strings.TrimSpace(resolved)
	baseCfg, err := loadBaseRepoConfig(ctx, workDir, base)
	if err != nil {
		return ResolvedBase{}, err
	}
	origin := strings.TrimSpace(pipeline.Origin)
	if origin == "" {
		origin = "origin not stated"
	}
	return ResolvedBase{
		CanonicalRef:    "the pipeline-supplied base",
		CanonicalBranch: strings.TrimSpace(pipeline.DefaultBranch),
		CanonicalCommit: base,
		Base:            base,
		Config:          baseCfg,
		Pipeline:        true,
		PipelineOrigin:  origin,
	}, nil
}

// resolveVerifiedBase establishes the canonical commit from the remote and
// takes the merge-base with head.
//
// The pin is read from the config at the provisionally resolved base, so an
// operator can move the canonical ref without the move being self-certifying:
// the ref that authorizes the change of ref is the previous canonical one.
func resolveVerifiedBase(ctx context.Context, workDir, headRef string) (ResolvedBase, error) {
	resolved, detectErr := canonicalBaseFor(ctx, workDir, headRef, config.SlopBaseRef{})
	if detectErr != nil {
		// A repository whose trunk is not called main or master has to be able
		// to say so, and the config that says so cannot be read until a base
		// exists.
		bootstrapped, bootstrapErr := bootstrapPinnedBase(ctx, workDir, headRef)
		if bootstrapErr != nil {
			return ResolvedBase{}, errors.Join(detectErr, bootstrapErr)
		}
		return bootstrapped, nil
	}
	pin := config.Merge(config.DefaultGlobalConfig(), resolved.Config).Slop.BaseRef
	if pin.Remote == "" && pin.Branch == "" {
		return resolved, nil
	}
	pinned, pinErr := canonicalBaseFor(ctx, workDir, headRef, pin)
	if pinErr != nil {
		return ResolvedBase{}, pinErr
	}
	if pinned.CanonicalRef == resolved.CanonicalRef {
		return resolved, nil
	}
	pinned.Pinned = true
	return pinned, nil
}

// bootstrapPinnedBase is the one route by which the head tree gets a say in
// which ref is canonical, and it is a dead end for an author.
//
// The head tree's copy of slop.base_ref supplies a CANDIDATE remote and branch,
// that branch is resolved on the remote, and the config at the resulting base
// must independently carry the same pin. Round 4 defeated the previous version
// of this by committing the pin with weak thresholds, pointing a local branch at
// that commit, and naming the local branch: the pin resolved locally, so the
// author's own commit certified itself. It cannot now, because the candidate is
// resolved by asking the remote for refs/heads/<branch>. Planting the pin still
// works, but only by pushing it to the operator's remote, which is the bar every
// other route already has to clear.
func bootstrapPinnedBase(ctx context.Context, workDir, headRef string) (ResolvedBase, error) {
	headCfg, err := loadBaseRepoConfig(ctx, workDir, headRef)
	if err != nil {
		return ResolvedBase{}, err
	}
	pin := config.Merge(config.DefaultGlobalConfig(), headCfg).Slop.BaseRef
	if pin.Remote == "" && pin.Branch == "" {
		return ResolvedBase{}, fmt.Errorf("no conventional default branch on the configured remote and no slop.base_ref at head to name one")
	}
	resolved, err := canonicalBaseFor(ctx, workDir, headRef, pin)
	if err != nil {
		return ResolvedBase{}, err
	}
	confirmed := config.Merge(config.DefaultGlobalConfig(), resolved.Config).Slop.BaseRef
	if confirmed != pin {
		return ResolvedBase{}, fmt.Errorf("slop.base_ref at head names %q but the config at %s does not, so the head is naming its own canonical ref; commit the pin to that ref's history first", resolved.CanonicalRef, resolved.Base)
	}
	resolved.Pinned = true
	return resolved, nil
}

// canonicalBaseFor asks the remote what the canonical branch points at and
// resolves the base from that commit alone.
func canonicalBaseFor(ctx context.Context, workDir, headRef string, pin config.SlopBaseRef) (ResolvedBase, error) {
	remote := strings.TrimSpace(pin.Remote)
	if remote == "" {
		remote = "origin"
	}
	url, err := remoteURL(ctx, workDir, remote)
	if err != nil {
		return ResolvedBase{}, err
	}
	branches := []string{"main", "master"}
	if branch := strings.TrimSpace(pin.Branch); branch != "" {
		branches = []string{branch}
	}
	reasons := make([]string, 0, len(branches))
	for _, branch := range branches {
		commit, lookupErr := remoteBranchCommit(ctx, workDir, url, branch)
		if lookupErr != nil {
			reasons = append(reasons, lookupErr.Error())
			continue
		}
		if _, err := git.Run(ctx, workDir, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil {
			return ResolvedBase{}, fmt.Errorf("resolve canonical base ref: %s/%s is %s on the remote, and this repository's object store does not hold that commit; fetch %s before gating", remote, branch, commit, remote)
		}
		base, err := mergeBase(ctx, workDir, commit, headRef)
		if err != nil {
			return ResolvedBase{}, err
		}
		baseCfg, err := loadBaseRepoConfig(ctx, workDir, base)
		if err != nil {
			return ResolvedBase{}, err
		}
		return ResolvedBase{
			CanonicalRef:    remote + "/" + branch,
			CanonicalBranch: branch,
			CanonicalCommit: commit,
			Base:            base,
			Config:          baseCfg,
		}, nil
	}
	return ResolvedBase{}, fmt.Errorf("resolve canonical base ref: %s", strings.Join(reasons, "; "))
}

// remoteURL names the remote the gate asks. A repository with no remote has no
// history outside the change under test, so there is nothing here to fall back
// to; the caller turns this into the unverified route rather than guessing.
func remoteURL(ctx context.Context, workDir, remote string) (string, error) {
	url, err := git.Run(ctx, workDir, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("resolve canonical base ref: this repository has no remote named %q", remote)
	}
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return "", fmt.Errorf("resolve canonical base ref: remote %q has no URL", remote)
	}
	return trimmed, nil
}

// remoteBranchCommit asks the remote for one branch by its FULL refname.
//
// The full refname matters. A short name lets the remote's own tag namespace
// answer for a branch, which is the same shadowing this whole path exists to
// remove, one hop further out. Anything other than exactly one refs/heads/
// answer is refused rather than picked from.
func remoteBranchCommit(ctx context.Context, workDir, url, branch string) (string, error) {
	refName := "refs/heads/" + branch
	output, err := git.Run(ctx, workDir, "ls-remote", "--heads", "--exit-code", url, refName)
	if err != nil {
		return "", fmt.Errorf("remote %s does not answer for %s: %v", url, refName, err)
	}
	var commit string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != refName {
			continue
		}
		if commit != "" {
			return "", fmt.Errorf("remote %s answered for %s more than once", url, refName)
		}
		commit = fields[0]
	}
	if !isObjectID(commit) {
		return "", fmt.Errorf("remote %s returned no usable commit for %s", url, refName)
	}
	return commit, nil
}

// isObjectID reports whether a string is a full hexadecimal object id. It
// exists so a remote's answer is parsed rather than interpolated.
func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

// unverifiedBase supplies something to DIFF when the canonical commit could not
// be established, and nothing else.
//
// The distinction is the whole point. A local ref may say what to compare
// against, because getting that wrong produces a confusing diff. It may never
// say how strictly to compare, because getting that wrong is the entire defect
// class: the caller carries no config from here, the tier is pinned to
// full-adversarial by risk.Config.BaseUnverified, and the run reports a
// base-ref-unverified finding so it cannot end at exit 0.
func unverifiedBase(ctx context.Context, workDir, headRef string, reason error) (ResolvedBase, error) {
	for _, candidate := range []string{"origin/main", "origin/master", "main", "master"} {
		commit, err := git.Run(ctx, workDir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		if err != nil {
			continue
		}
		base, err := mergeBase(ctx, workDir, strings.TrimSpace(commit), headRef)
		if err != nil {
			continue
		}
		return ResolvedBase{
			CanonicalRef: candidate,
			Base:         base,
			Config:       &config.RepoConfig{},
			Unverified:   reason.Error(),
		}, nil
	}
	return ResolvedBase{}, fmt.Errorf("%w, and no local main or master names a commit to diff against either", reason)
}

func mergeBase(ctx context.Context, workDir, canonicalCommit, headRef string) (string, error) {
	base, err := git.Run(ctx, workDir, "merge-base", canonicalCommit, headRef)
	if err != nil {
		return "", fmt.Errorf("resolve base revision: %s and the head revision share no history: %w", canonicalCommit, err)
	}
	return strings.TrimSpace(base), nil
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

// formatFinding labels a blocking finding and a non-blocking notice
// differently, so a reader never has to reconcile a printed "finding:" line
// with a passing verdict.
func formatFinding(finding engine.Finding) string {
	label := "finding"
	if !finding.Blocks() {
		label = "notice"
	}
	location := finding.Path
	if location != "" && finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", location, finding.Line)
	}
	if location != "" {
		return fmt.Sprintf("%s: [%s] %s: %s", label, finding.Lens, location, finding.Description)
	}
	return fmt.Sprintf("%s: [%s] %s", label, finding.Lens, finding.Description)
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
	fmt.Fprintln(output, "usage: noslop gate [--repo DIR] [--head REF] [--intent TEXT] [--tier TIER] [--force-tier] [--thread URL] [--blocklist FILE] [--provider NAME] [--model NAME] [--reasoning-effort LEVEL] [--lane-id ID] [--change-class CLASS]")
	fmt.Fprintln(output, "       the base revision is not a flag: it comes from the configured remote, or from an orchestrating pipeline")
	fmt.Fprintln(output, "       noslop evaluate --corpus DIR [--case-set FILE] --unconditioned-results FILE --conditioned-results FILE")
}
