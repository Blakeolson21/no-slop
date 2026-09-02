package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/identity"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
)

const axiPlanTrustedRef = "refs/no-slop/plan/trusted"

type axiRunPlan struct {
	Skip           []types.StepName `json:"skip"`
	Yes            bool             `json:"yes"`
	Intent         string           `json:"intent"`
	PositionalArgs []string         `json:"positional_args"`
	Repo           axiPlanRepo      `json:"repo"`
	Branch         string           `json:"branch"`
	HeadSHA        string           `json:"head_sha"`
	Agent          axiPlanAgent     `json:"agent"`
}

type axiPlanRepo struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	Upstream         string `json:"upstream"`
	DefaultBranch    string `json:"default_branch"`
	TrustedConfigSHA string `json:"trusted_config_sha"`
}

type axiPlanAgent struct {
	Primary   types.AgentName   `json:"primary"`
	Fallbacks []types.AgentName `json:"fallbacks"`
	Lanes     []axiPlanLane     `json:"lanes"`
}

type axiPlanLane struct {
	Configured types.AgentName `json:"configured"`
	Resolved   string          `json:"resolved"`
	Seat       axiPlanSeat     `json:"seat"`
}

type axiPlanSeat struct {
	Source    string `json:"source"`
	Pool      string `json:"pool,omitempty"`
	Selection string `json:"selection"`
}

func newAxiPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "plan [proposed axi run flags]",
		Short:              "Print the effective configuration for a proposed run without starting it",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAxiPlan(cmd, args)
		},
	}
}

func runAxiPlan(cmd *cobra.Command, proposedArgs []string) error {
	// Parse with the command that actually executes a run. This deliberately
	// does not register a second set of flags: Cobra/Pflag remains the only
	// grammar for boolean values, terminators, repeats, and future run flags.
	runCmd := newAxiRunCmd()
	if err := acceptProposedRunInvocation(runCmd, proposedArgs); err != nil {
		return &exitError{code: 2, err: fmt.Errorf("parse proposed axi run invocation: %w", err)}
	}

	yes, err := runCmd.Flags().GetBool("yes")
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("read parsed --yes: %w", err)}
	}
	skipValue, err := runCmd.Flags().GetString("skip")
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("read parsed --skip: %w", err)}
	}
	skip, err := parseSkipSteps(skipValue)
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("parse proposed axi run invocation: %w", err)}
	}
	intent, err := runCmd.Flags().GetString("intent")
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("read parsed --intent: %w", err)}
	}

	plan, err := resolveAxiRunPlan(cmd.Context())
	if err != nil {
		return &exitError{code: 1, err: fmt.Errorf("resolve effective config: %w", err)}
	}
	plan.Yes = yes
	plan.Skip = append([]types.StepName{}, skip...)
	plan.Intent = intent
	plan.PositionalArgs = append([]string{}, runCmd.Flags().Args()...)

	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(plan); err != nil {
		return &exitError{code: 1, err: fmt.Errorf("encode effective config: %w", err)}
	}
	return nil
}

// acceptProposedRunInvocation applies every acceptance check the run command's
// own flag set performs: Pflag parsing plus the Cobra required-flag and
// flag-group validation that `axi run` reaches after parsing. Parsing alone
// would accept a proposal the real command rejects the moment a run flag is
// marked required or grouped, which is the divergence a second grammar would
// have introduced anyway.
//
// The Args validator is deliberately not applied. Post-terminator tokens are
// reported as positional_args so the caller can see how this flag set read
// them; classifying them is the whole point of the report.
func acceptProposedRunInvocation(runCmd *cobra.Command, proposedArgs []string) error {
	if err := runCmd.ParseFlags(proposedArgs); err != nil {
		return err
	}
	if err := runCmd.ValidateRequiredFlags(); err != nil {
		return err
	}
	return runCmd.ValidateFlagGroups()
}

func resolveAxiRunPlan(ctx context.Context) (axiRunPlan, error) {
	p, err := paths.New()
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("resolve paths: %w", err)
	}
	database, err := db.OpenReadOnly(p.DB())
	if err != nil {
		if os.IsNotExist(err) {
			return axiRunPlan{}, fmt.Errorf("repo not initialized (run 'no-slop init' first)")
		}
		return axiRunPlan{}, fmt.Errorf("open gate registry read-only: %w", err)
	}
	defer database.Close()

	repo, err := findRepo(database)
	if err != nil {
		return axiRunPlan{}, err
	}
	workDir, err := git.FindGitRoot(".")
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("find repository root: %w", err)
	}
	branch, err := git.CurrentBranch(ctx, workDir)
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("get current branch: %w", err)
	}
	if branch == "HEAD" {
		return axiRunPlan{}, fmt.Errorf("detached HEAD: check out a branch before planning")
	}
	headSHA, err := git.Run(ctx, workDir, "rev-parse", "HEAD")
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("get current HEAD: %w", err)
	}
	if strings.TrimSpace(repo.DefaultBranch) == "" {
		return axiRunPlan{}, fmt.Errorf("repository has no known default branch to read trusted config from")
	}

	globalCfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("load global config: %w", err)
	}
	pushedCfg, _, err := loadRepoConfigAtRef(ctx, workDir, headSHA)
	if err != nil {
		return axiRunPlan{}, fmt.Errorf("load proposed branch config at %s: %w", headSHA, err)
	}

	trustedCfg, trustedSHA, err := fetchTrustedRepoConfigForPlan(ctx, workDir, repo.DefaultBranch)
	if err != nil {
		return axiRunPlan{}, err
	}
	allowRepoCommands := trustedCfg != nil && trustedCfg.AllowRepoCommands
	effectiveRepoCfg := config.EffectiveRepoConfig(pushedCfg, trustedCfg, allowRepoCommands)
	resolvedCfg := config.Merge(globalCfg, effectiveRepoCfg)
	if err := p.ValidateEvidenceRoot(resolvedCfg.Test.Evidence.LocalRoot); err != nil {
		return axiRunPlan{}, err
	}
	resolvedCfg.TrustedConfigSHA = trustedSHA
	if err := resolvedCfg.ResolveAgent(ctx, exec.LookPath); err != nil {
		return axiRunPlan{}, fmt.Errorf("resolve agent: %w", err)
	}

	resolvedAgents := append([]types.AgentName{}, resolvedCfg.Agents...)
	if len(resolvedAgents) == 0 {
		resolvedAgents = []types.AgentName{resolvedCfg.Agent}
	}
	lanes := make([]axiPlanLane, 0, len(resolvedAgents))
	for _, configured := range resolvedAgents {
		resolvedLane := agent.LaneName(configured)
		seat := axiPlanSeat{Source: "process-environment", Selection: "current"}
		if resolvedCfg.Quartermaster.Enabled {
			if pool, ok := agent.QuartermasterPoolForLane(resolvedLane); ok {
				seat = axiPlanSeat{Source: "quartermaster", Pool: pool, Selection: "deferred-until-invocation"}
			}
		}
		lanes = append(lanes, axiPlanLane{Configured: configured, Resolved: resolvedLane, Seat: seat})
	}

	return axiRunPlan{
		Skip:           []types.StepName{},
		PositionalArgs: []string{},
		Repo: axiPlanRepo{
			ID:               repo.ID,
			Path:             repo.WorkingPath,
			Upstream:         safeurl.Redact(repo.UpstreamURL),
			DefaultBranch:    repo.DefaultBranch,
			TrustedConfigSHA: trustedSHA,
		},
		Branch:  branch,
		HeadSHA: headSHA,
		Agent: axiPlanAgent{
			Primary:   resolvedCfg.Agent,
			Fallbacks: resolvedAgents,
			Lanes:     lanes,
		},
	}, nil
}

func fetchTrustedRepoConfigForPlan(ctx context.Context, workDir, defaultBranch string) (*config.RepoConfig, string, error) {
	originURL, err := git.GetRemoteURL(ctx, workDir, "origin")
	if err != nil {
		return nil, "", fmt.Errorf("read origin for trusted default branch: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "no-slop-plan-")
	if err != nil {
		return nil, "", fmt.Errorf("create temporary trusted-config repository: %w", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	if _, err := git.Run(ctx, tmpDir, "init", "--bare", "."); err != nil {
		return nil, "", fmt.Errorf("initialize temporary trusted-config repository: %w", err)
	}
	if err := git.FetchRemoteBranchToPrivateRef(ctx, tmpDir, originURL, defaultBranch, axiPlanTrustedRef); err != nil {
		return nil, "", fmt.Errorf("fetch trusted default branch %q: %w", defaultBranch, err)
	}
	trustedSHA, err := git.ResolveRef(ctx, tmpDir, axiPlanTrustedRef)
	if err != nil {
		return nil, "", fmt.Errorf("resolve trusted default branch %q: %w", defaultBranch, err)
	}
	trustedCfg, present, err := loadRepoConfigAtRef(ctx, tmpDir, trustedSHA)
	if err != nil {
		return nil, "", fmt.Errorf("load trusted config at %s: %w", trustedSHA, err)
	}
	if !present {
		trustedCfg = nil
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, "", fmt.Errorf("remove temporary trusted-config repository: %w", err)
	}
	cleaned = true
	return trustedCfg, trustedSHA, nil
}

func loadRepoConfigAtRef(ctx context.Context, repoDir, ref string) (*config.RepoConfig, bool, error) {
	var canonicalData, legacyData []byte
	var canonicalPresent, legacyPresent bool
	for _, candidate := range []struct {
		name    string
		present *bool
		data    *[]byte
	}{
		{name: identity.RepoConfigName, present: &canonicalPresent, data: &canonicalData},
		{name: identity.LegacyRepoConfigName, present: &legacyPresent, data: &legacyData},
	} {
		entry, err := git.Run(ctx, repoDir, "ls-tree", ref, "--", candidate.name)
		if err != nil {
			return nil, false, err
		}
		if entry == "" {
			continue
		}
		content, err := git.ShowFile(ctx, repoDir, ref, candidate.name)
		if err != nil {
			return nil, false, err
		}
		*candidate.present = true
		*candidate.data = []byte(content)
	}
	return config.LoadRepoFromAliasBytes(canonicalData, canonicalPresent, legacyData, legacyPresent)
}
