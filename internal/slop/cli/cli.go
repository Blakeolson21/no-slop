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
	"github.com/Blakeolson21/no-slop/internal/safeurl"
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
	var drift []engine.ConfigDrift
	if resolvedBase.Resolved() {
		for _, detail := range slopConfigDrift(config.Merge(globalCfg, headRepoCfg).Slop, cfg.Slop) {
			drift = append(drift, engine.ConfigDrift{
				Path:   identity.RepoConfigName,
				Detail: detail + "; land the slop.* change on the base branch first, because a config change cannot certify itself",
			})
		}
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
	blocklistEntries, blocklistState, blocklistDrift, err := loadBlocklist(ctx, workDir, baseRef, blocklistPath, blocklistConfigured, resolvedBase.Resolved())
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, blocklistState)
	if blocklistDrift != nil {
		drift = append(drift, *blocklistDrift)
	}
	if override := strings.TrimSpace(*blocklist); override != "" {
		// The override reads the worktree and only ever ADDS names, so it cannot
		// weaken the base ref's list and does not join the drift comparison. It
		// is still named on its own line: adding names from a file the change
		// under test can write is a fact a reviewer should see.
		extraEntries, extraState, _, extraErr := loadBlocklist(ctx, workDir, baseRef, override, true, false)
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
				JudgedContent:   judgedContent(changes),
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
		JudgedContent:   judgedContent(changes),
		ChangeID:        baseRef + ".." + headRef,
	}, result.Decision, result, outcome)); err != nil {
		printChecks(stdout, result)
		return fmt.Errorf("record provenance: %w", err)
	}
	printResult(stdout, result, resolvedBase.Certifying())
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
	// JudgedContent is derived from the changed paths, never from a flag. See
	// judgedContent and provenance.LensScores.
	JudgedContent []string
}

func provenanceRecord(input provenanceInput, decision risk.Decision, result engine.Result, outcome string) provenance.Record {
	byLens := make(map[string]provenance.LensFindings)
	for _, finding := range result.Findings {
		current := byLens[finding.Lens]
		recorded := provenance.Finding{
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		}
		// A non-blocking finding is recorded but never scored. Filing it as
		// accepted made the one severity that deliberately does not fail the run
		// pin its record past retention and drive the lens escalation, which only
		// a reviewed clean pass clears.
		if finding.Blocks() {
			current.Accepted = append(current.Accepted, recorded)
		} else {
			current.Noticed = append(current.Noticed, recorded)
		}
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
		JudgedContent:   input.JudgedContent,
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
	data, present, err := readBaseFile(ctx, workDir, baseRef, name)
	if err != nil {
		return nil, false, fmt.Errorf("read base repo config: %w", err)
	}
	return data, present, nil
}

// readBaseFile reads one tracked path out of the base revision, reporting
// separately whether the path exists there at all. Absent is a valid answer and
// unreadable is not: an input the gate reads its strength from must never
// resolve to the permissive default because a git command failed.
//
// git.Output rather than git.Run, because blob content is data and trailing
// whitespace in it is data too.
func readBaseFile(ctx context.Context, workDir, baseRef, name string) ([]byte, bool, error) {
	listing, err := git.Output(ctx, workDir, "ls-tree", "--name-only", "-z", baseRef, "--", name)
	if err != nil {
		return nil, false, fmt.Errorf("read listing for %s at %s: %w", name, baseRef, err)
	}
	if strings.TrimSpace(strings.ReplaceAll(listing, "\x00", "")) == "" {
		return nil, false, nil
	}
	content, err := git.Output(ctx, workDir, "show", baseRef+":"+name)
	if err != nil {
		return nil, false, fmt.Errorf("read %s at %s: %w", name, baseRef, err)
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

// judgedContent names the kinds of content this run actually looked at.
//
// It is derived from the changed paths and deliberately ignores
// --change-class. The recorded change class exists for a reader and is a string
// the caller asserts about itself; this decides whether a later clean pass
// counts as evidence about a lens, and every self-asserted input this product
// has trusted with a decision has been an evasion route. See
// provenance.LensScores.
//
// A path is counted once per kind it belongs to, and a test file counts as
// tests rather than as source: the question a lens asks about tests is asked of
// test files.
func judgedContent(changes []engine.Change) []string {
	var source, tests, docs bool
	for _, change := range changes {
		path := strings.ToLower(filepath.ToSlash(change.Path))
		extension := filepath.Ext(path)
		switch {
		case strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, ".test.") ||
			strings.Contains(path, ".spec.") ||
			strings.HasPrefix(filepath.Base(path), "test_") ||
			strings.Contains(path, "/test/") ||
			strings.Contains(path, "/tests/") ||
			strings.HasPrefix(path, "test/") ||
			strings.HasPrefix(path, "tests/"):
			tests = true
		case extension == ".md" || extension == ".mdx" || strings.HasPrefix(path, "docs/"):
			docs = true
		default:
			source = true
		}
	}
	content := make([]string, 0, 3)
	if source {
		content = append(content, provenance.ContentSource)
	}
	if tests {
		content = append(content, provenance.ContentTests)
	}
	if docs {
		content = append(content, provenance.ContentDocs)
	}
	return content
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

// loadBlocklist reads the private-name blocklist, preferring the copy at the
// base ref over the copy in the head worktree.
//
// The path came from the base config from the moment the boundary was drawn,
// and the CONTENT did not, which made the blocklist the one gate-strength input
// the change under test still supplied. `.noslop-blocklist` is not a field of
// config.Slop, so the reflective drift comparison never saw it either: a single
// `: > .noslop-blocklist` disarmed an operator's identity policy and the run
// reported a clean gate config and a clean leak scan.
//
// So the content joins the boundary. When the file is tracked at the base ref,
// the base copy is the one in force and a differing head copy is DRIFT, named
// in both directions for the same reason a slop.* change is: the run that would
// bless the edit is the run the edit reconfigures.
//
// The documented local case survives, because it has to. `.noslop-blocklist` is
// documented as private data that is gitignored, and a repository using it that
// way has nothing at the base ref to read. Such a run keeps reading the
// worktree and SAYS SO on its own state line, which is the honest report: that
// half of the identity scan sits outside the base-ref boundary and its only
// defence is the printed entry count.
//
// A configured path that resolves OUTSIDE the worktree is not in that case at
// all. It is a file the change under test cannot edit, so it is operator-owned
// already and there is no base-ref copy of it to prefer; asking git for one made
// a working `../shared/.noslop-blocklist` abort the whole run at exit 2 with an
// error naming git plumbing rather than the config key. The split is by where
// the path resolves rather than by whether it is spelled absolutely, because an
// absolute path can still land inside the worktree, and that copy is one the
// change can edit.
func loadBlocklist(ctx context.Context, workDir, baseRef, configured string, explicitlyConfigured, baseReadable bool) ([]string, string, *engine.ConfigDrift, error) {
	if configured == "" {
		return nil, "leak scan: no private-name blocklist configured", nil, nil
	}
	state := "default"
	if explicitlyConfigured {
		state = "configured"
	}
	relative, insideTree := repoRelativePath(workDir, configured)
	trackedAtBase := false
	var baseContent []byte
	if baseReadable && insideTree {
		data, present, err := readBaseFile(ctx, workDir, baseRef, relative)
		if err != nil {
			return nil, "", nil, err
		}
		baseContent, trackedAtBase = data, present
	}

	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	headContent, headErr := os.ReadFile(path)

	if trackedAtBase {
		entries := leakscan.ParseBlocklist(string(baseContent))
		var drift *engine.ConfigDrift
		if headErr == nil && string(headContent) != string(baseContent) {
			headEntries := leakscan.ParseBlocklist(string(headContent))
			drift = &engine.ConfigDrift{
				Path: configured,
				// The entry counts are named and the entries never are. This
				// file is a list of private identities, so printing what
				// changed would put them on stdout and into the provenance
				// record, which is the failure the whole check exists to stop.
				Detail: fmt.Sprintf(
					"the private-name blocklist has %d entries at head and %d at the base ref; the base ref's copy is the one in force, and a head edit to it cannot change how strictly this run scans. Land the blocklist change on the base branch first",
					len(headEntries), len(entries)),
			}
		}
		return entries, fmt.Sprintf("leak scan: loaded %s private-name blocklist from %s at the base ref (%d entries)", state, configured, len(entries)), drift, nil
	}

	if headErr != nil {
		if errors.Is(headErr, os.ErrNotExist) && !explicitlyConfigured {
			return nil, fmt.Sprintf("leak scan: no private-name blocklist (default path %s not present)", configured), nil, nil
		}
		if insideTree && baseReadable {
			return nil, "", nil, fmt.Errorf("read private-name blocklist (%s path %s): %w; a blocklist inside the repository must be tracked at the base ref %s or readable in the worktree", state, configured, headErr, baseRef)
		}
		return nil, "", nil, fmt.Errorf("read private-name blocklist (%s path %s): %w", state, configured, headErr)
	}
	// The entry count is printed because a readable file with no entries scans
	// exactly like a missing one, and an operator reading only "loaded" cannot
	// tell a working identity policy from an empty one. It is the only defence
	// these branches have, which is why each line says where the content came
	// from and how much of the boundary it sits inside.
	entries := leakscan.ParseBlocklist(string(headContent))
	if !insideTree {
		return entries, fmt.Sprintf("leak scan: loaded %s private-name blocklist from %s, which resolves outside the repository worktree and so cannot be edited by the change under test (%d entries)", state, configured, len(entries)), nil, nil
	}
	return entries, fmt.Sprintf("leak scan: loaded %s private-name blocklist from %s in the head worktree, which is not tracked at the base ref, so its content is outside the base-ref boundary (%d entries)", state, configured, len(entries)), nil, nil
}

// repoRelativePath answers whether a configured path resolves inside the
// worktree, and if so gives it in the slash-separated form a git pathspec needs.
//
// Outside is the honest answer for a path git would reject anyway: `git ls-tree
// -- ../shared/list` fails with "outside repository" and exit 128, and turning
// that into a gate abort punished a config that worked before the boundary was
// drawn. The symlink retry is the macOS case where the worktree is reached
// through /var and the config names /private/var, or the reverse: resolving the
// alias keeps such a path inside the tree, which is the fail-closed direction.
func repoRelativePath(workDir, configured string) (string, bool) {
	if relative, ok := relativeInside(workDir, configured); ok {
		return relative, true
	}
	resolvedDir, dirErr := filepath.EvalSymlinks(workDir)
	target := configured
	if !filepath.IsAbs(target) {
		target = filepath.Join(workDir, target)
	}
	resolvedTarget, targetErr := filepath.EvalSymlinks(filepath.Dir(target))
	if dirErr != nil || targetErr != nil {
		return "", false
	}
	return relativeInside(resolvedDir, filepath.Join(resolvedTarget, filepath.Base(target)))
}

func relativeInside(workDir, configured string) (string, bool) {
	target := configured
	if !filepath.IsAbs(target) {
		target = filepath.Join(workDir, target)
	}
	relative, err := filepath.Rel(workDir, target)
	if err != nil {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false
	}
	return relative, true
}

// advisoryBanner is the sentence a run prints in place of the certification it
// is not making. Every route except an orchestrator-supplied base carries it,
// because every other route resolves the base from inside a repository the
// author of the change under test controls.
const advisoryBanner = "advisory: base supplied by this repository; not a certification"

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
	// RemoteURL is the URL this run actually asked, after git applied every
	// insteadOf rewrite it knows about, with any userinfo redacted. The round-5
	// header named neither the URL nor the rewrite, so a run against
	// /tmp/evil.git printed the same sentence as a run against the operator's
	// forge. It is disclosure only: naming the URL does not make it trustworthy,
	// which is the whole reason this route can no longer certify.
	RemoteURL string
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

// Resolved reports whether a canonical commit came back from a remote at all.
//
// It was called Verified until round 5, and the rename is the finding. What
// asking a remote establishes is that some repository answered for a branch
// name, which is not evidence about whose repository it was: the URL comes from
// .git/config, from the remote list, or from an insteadOf rewrite in the
// ambient environment, and the author of the change under test writes all
// three. Whether a run may certify is a separate question with a separate
// answer; see Certifying.
func (r ResolvedBase) Resolved() bool { return r.Unverified == "" }

// Certifying reports whether this run may render a verdict.
//
// It is a method over the resolution route rather than a field, so there is no
// value anywhere for a later change to set by hand. Exactly one route answers
// true, and the argument is the round-5 theorem: every input a standalone run
// could resolve a base from lives inside the repository the author of the
// change under test controls. Refs are author-writable, .git/config is
// author-writable, the remote list is author-writable, and the ambient
// GIT_CONFIG_* environment is author-writable while writing nothing to disk at
// all. Five rounds hardened five instances of that and the control moved one
// input further out each time.
//
// An orchestrating pipeline is outside that boundary because it carved the
// worktree and resolved the trunk before the author's process existed, and
// reaching the channel requires being in-process with the gate.
func (r ResolvedBase) Certifying() bool { return r.Pipeline }

// String is the run header's base line. Each route reads differently on
// purpose. The previous header printed "base: <sha> from merge-base with
// origin/main" whether origin/main was the operator's remote branch or a local
// branch the author had created ten seconds earlier under that name, and the
// round-4 replacement printed "verified by ls-remote against the configured
// remote" without ever naming which remote that was, so a run redirected to
// /tmp/evil.git by one environment variable read exactly like an honest one.
func (r ResolvedBase) String() string {
	switch {
	case r.Pipeline:
		return fmt.Sprintf("base: %s supplied by the orchestrating pipeline (%s); no local ref and no flag took part", r.Base, r.PipelineOrigin)
	case !r.Resolved():
		return fmt.Sprintf("base: %s from merge-base with the LOCAL ref %s, UNVERIFIED: %s. Gate strength is pinned to full-adversarial and every slop.* value came from built-in defaults rather than from that commit; %s", r.Base, r.CanonicalRef, r.Unverified, advisoryBanner)
	}
	pinned := ""
	if r.Pinned {
		pinned = ", pinned by slop.base_ref"
	}
	return fmt.Sprintf("base: %s from merge-base with %s at %s, resolved by ls-remote against %s%s; %s", r.Base, r.CanonicalRef, r.CanonicalCommit, r.RemoteURL, pinned, advisoryBanner)
}

// resolveBase decides which commit supplies the gate's strength, and whether
// the run that follows is allowed to certify anything.
//
// THE ROUND-5 THEOREM. Any base a run resolves from inside a repository the
// author of the change under test controls is a base that author can choose.
// Five rounds proved it by exhaustion rather than by argument, each one closing
// the named instance and each one finding the control one input further out:
// the `--base` flag, then a local ref wearing the canonical name, then
// `refs/remotes/*`, then `remote.origin.url` in `.git/config`, and finally a
// `url.<X>.insteadOf` pair in the ambient GIT_CONFIG_* environment, which
// leaves nothing on disk, does not show in `git status`, and is gone when the
// process exits. Refs, config, remotes, and environment are all things the
// author writes, and there is no sixth input hiding behind them that is not.
//
// So the capability is removed rather than the instance. There are two modes:
//
//   - CERTIFYING, reached only through Options.PipelineBase. An orchestrator
//     carved the worktree and resolved the trunk before the author's process
//     existed, and reaching that channel requires being in-process with the
//     gate. This is the only mode that may print "verdict:".
//   - ADVISORY, every other route. The run does all the same work, reports
//     every finding, and exits non-zero on a blocking one; what it may not do
//     is certify, because the commit it judged against is one the theorem says
//     the author could have chosen. ls-remote resolution stays because a base
//     resolved from the operator's usual remote is the most useful thing to
//     diff against, and because a run with no base at all can say almost
//     nothing. It stays as convenience, not as evidence.
//
// The history below is kept because it is the argument for why this shape is
// the answer rather than a sixth hardening of a sixth input.
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
//     channel requires being in-process with the gate. CERTIFYING.
//  2. The network. `git ls-remote <configured remote URL> refs/heads/<branch>`
//     is asked what the canonical branch points at, and the merge-base is taken
//     against the commit the remote answered with. No local ref participates,
//     under any name. ADVISORY: the URL is one the author writes, so the answer
//     is something to diff against and nothing more.
//  3. Nothing. Offline, or with no remote, or with a remote that will not
//     answer, the run is pinned to the full tier, reads built-in defaults
//     instead of a base config it cannot trust, and fails with the reason
//     named. It never lowers, which is the S1 move: the cheap route is removed
//     rather than made conditional.
//
// `--base` is gone rather than validated, because validating it is the fix
// shape that failed twice.
//
// slop.base_ref.remote is NOT a mitigation and must not be documented as one.
// It was presented as making the choice of remote an operator decision, and it
// cannot be: resolveVerifiedBase resolves a provisional base with an empty pin
// first, so an author who has already repointed origin supplies the base the
// pin is then read from. Its honest description is a convenience for a
// repository whose trunk is not called main or master.
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
			// Redacted because a remote URL routinely carries userinfo, and a
			// gate that printed a credential into its own run header and its own
			// provenance record would be leaking exactly what its leak scan
			// exists to catch. internal/safeurl is the one owner of that.
			RemoteURL: safeurl.Redact(url),
			Base:      base,
			Config:    baseCfg,
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
	qualifiers := make([]string, 0, 3)
	if len(check.Unarmed) > 0 {
		qualifiers = append(qualifiers, "not armed: "+strings.Join(check.Unarmed, "; "))
	}
	if len(check.Degraded) > 0 {
		qualifiers = append(qualifiers, "reduced coverage: "+strings.Join(check.Degraded, "; "))
	}
	if len(check.Widened) > 0 {
		qualifiers = append(qualifiers, "read more than one way: "+strings.Join(check.Widened, "; "))
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

// printResult prints the run's outcome, and it is the single place the product
// decides whether a run may certify anything.
//
// "verdict:" is reserved for a certifying run. A run whose base came from the
// repository under test prints an advisory line instead, and the two vocabularies
// are deliberately disjoint so that grepping a log for "verdict:" finds every
// certification and nothing else. The advisory line is not a softer verdict: it
// reports the same work, over the same checks, with the same exit code, and
// says only that the commit it judged against is one the author could have
// chosen.
func printResult(stdout io.Writer, result engine.Result, certifying bool) {
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
	switch {
	case certifying && result.Passed:
		fmt.Fprintln(stdout, "verdict: pass")
	case certifying:
		fmt.Fprintln(stdout, "verdict: fail")
	case result.Passed:
		fmt.Fprintf(stdout, "advisory-clean; %s\n", advisoryBanner)
	default:
		fmt.Fprintf(stdout, "advisory-blocked; %s\n", advisoryBanner)
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "NoSlop is the reviewer that knows the author is an AI.")
	fmt.Fprintln(output, "usage: noslop gate [--repo DIR] [--head REF] [--intent TEXT] [--tier TIER] [--force-tier] [--thread URL] [--blocklist FILE] [--provider NAME] [--model NAME] [--reasoning-effort LEVEL] [--lane-id ID] [--change-class CLASS]")
	fmt.Fprintln(output, "       the base revision is not a flag: it comes from the configured remote, or from an orchestrating pipeline")
	fmt.Fprintln(output, "       a standalone run is ADVISORY and cannot certify, because the base it resolved came from this repository;")
	fmt.Fprintln(output, "       only a base supplied by an orchestrator prints a verdict")
	fmt.Fprintln(output, "       noslop evaluate --corpus DIR [--case-set FILE] --unconditioned-results FILE --conditioned-results FILE")
}
