package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/daemon"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/Blakeolson21/no-slop/internal/winproc"
	"github.com/spf13/cobra"
)

type doctorAgentCheck struct {
	name     string
	binaries []string
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check system health and dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackCommandStatus("doctor", func() (string, error) {
				w := cmd.OutOrStdout()
				allOK := true

				ok := func(label, detail string) {
					fmt.Fprintf(w, "  %s %s  %s\n", sGreen.Render("✓"), sDim.Render(label), detail)
				}
				warn := func(label, detail string) {
					fmt.Fprintf(w, "  %s %s  %s\n", sYellow.Render("–"), sDim.Render(label), detail)
				}
				fail := func(label, detail string) {
					fmt.Fprintf(w, "  %s %s  %s\n", sRed.Render("✗"), sDim.Render(label), detail)
				}

				fmt.Fprintf(w, "  %s\n", sCyan.Render("System"))

				if _, err := exec.LookPath("git"); err != nil {
					fail("git           ", "not found")
					allOK = false
				} else {
					gitCtx, cancelGit := git.BoundContext(cmd.Context(), "--version")
					defer cancelGit()
					gitCmd := exec.CommandContext(gitCtx, "git", "--version")
					gitCmd.WaitDelay = git.CommandWaitDelay()
					winproc.Harden(gitCmd)
					out, err := gitCmd.Output()
					if err != nil {
						fail("git           ", fmt.Sprintf("error (%v)", err))
						allOK = false
					} else {
						ok("git           ", strings.TrimSpace(string(out)))
					}
				}

				if _, err := exec.LookPath("gh"); err != nil {
					warn("gh            ", "not found "+sDim.Render("(optional, needed for PR/CI)"))
				} else {
					ok("gh            ", "ok")
				}

				if _, err := exec.LookPath("az"); err != nil {
					warn("az            ", "not found "+sDim.Render("(optional, needed for Azure DevOps PR/CI)"))
				} else {
					ok("az            ", "ok")
				}

				p, err := paths.New()
				if err != nil {
					fail("data directory", fmt.Sprintf("error resolving paths (%v)", err))
					allOK = false
				} else if _, err := os.Stat(p.Root()); os.IsNotExist(err) {
					fail("data directory", fmt.Sprintf("not found (%s)", p.Root()))
					allOK = false
				} else {
					ok("data directory", p.Root())
				}

				if p != nil {
					if _, err := os.Stat(p.DB()); os.IsNotExist(err) {
						warn("database      ", "not found "+sDim.Render("(will be created on first use)"))
					} else {
						d, err := db.Open(p.DB())
						if err != nil {
							fail("database      ", fmt.Sprintf("error (%v)", err))
							allOK = false
						} else {
							d.Close()
							ok("database      ", "ok")
						}
					}
				}

				if p != nil {
					alive, _ := daemon.IsRunning(p)
					if alive {
						ok("daemon        ", "running")
					} else {
						warn("daemon        ", "stopped")
					}
				}

				// Scoped quota marks are reported separately from installed agents:
				// one exhausted account/model does not make every account on its
				// provider lane unavailable.
				var liveOutages []lanehealth.Outage
				if p != nil {
					liveOutages = lanehealth.NewStore(p.LaneHealthFile(), nil).Snapshot()
				}
				quotaDetail := func(outage lanehealth.Outage) string {
					return fmt.Sprintf("quota-exhausted until %s (account %s, model %s; only this account/model is skipped, probed hourly)",
						outage.ResetTime(),
						shortQuotaAccountID(outage.AccountID), outage.Model)
				}

				agents := doctorAgentChecks()
				fmt.Fprintln(w)
				fmt.Fprintf(w, "  %s\n", sCyan.Render("Agents"))
				for _, a := range agents {
					label := fmt.Sprintf("%-14s", a.name)
					var found, missing []string
					for _, bin := range a.binaries {
						if path, err := exec.LookPath(bin); err != nil {
							missing = append(missing, bin)
						} else {
							found = append(found, path)
						}
					}
					switch {
					case len(missing) == 0:
						ok(label, strings.Join(found, ", "))
					case len(a.binaries) > 1:
						warn(label, "not found ("+strings.Join(missing, ", ")+")")
					default:
						warn(label, "not found")
					}
				}

				for _, outage := range liveOutages {
					warn(fmt.Sprintf("%-14s", outage.Lane), quotaDetail(outage))
				}

				if p == nil {
					fail("gate validation", "unavailable: data directory could not be resolved")
					allOK = false
				} else {
					globalCfg, err := config.LoadGlobal(p.ConfigFile())
					if err != nil {
						fail("gate validation", fmt.Sprintf("unavailable: load config (%v)", err))
						allOK = false
					} else {
						cfg := config.Merge(globalCfg, &config.RepoConfig{})
						if err := cfg.ResolveAgent(cmd.Context(), exec.LookPath); err != nil {
							fail("gate validation", err.Error())
							allOK = false
						} else if cfg.Quartermaster.Enabled {
							ok("gate validation", fmt.Sprintf("%s is runnable; account/model health is checked after each seat is selected", cfg.Agent))
						} else if outage, exhausted := configuredAgentOutage(cfg, p.LaneHealthFile()); exhausted {
							warn("gate validation", fmt.Sprintf("%s %s", cfg.Agent, quotaDetail(outage)))
						} else {
							ok("gate validation", fmt.Sprintf("%s is runnable", cfg.Agent))
						}
					}
				}

				if !allOK {
					fmt.Fprintln(w)
					fmt.Fprintf(w, "  %s\n", sRed.Render("some checks failed"))
					return "error", nil
				}

				return "success", nil
			})
		},
	}
}

func configuredAgentOutage(cfg *config.Config, statePath string) (lanehealth.Outage, bool) {
	if cfg == nil || statePath == "" {
		return lanehealth.Outage{}, false
	}
	a, err := agent.NewWithOptions(cfg.Agent, cfg.AgentPathFor(cfg.Agent), cfg.AgentArgsFor(cfg.Agent), agent.Options{
		ACPRegistryOverrides:   cfg.ACPRegistryOverrides,
		DisableProjectSettings: cfg.DisableProjectSettings,
	})
	if err != nil {
		return lanehealth.Outage{}, false
	}
	defer a.Close()
	scope, ok := agent.ResolveQuotaScope(a, agent.RunOpts{})
	if !ok {
		return lanehealth.Outage{}, false
	}
	return lanehealth.NewStore(statePath, nil).Outage(scope.Key)
}

func shortQuotaAccountID(accountID string) string {
	if len(accountID) > 12 {
		return accountID[:12]
	}
	return accountID
}

func doctorAgentChecks() []doctorAgentCheck {
	agents := []doctorAgentCheck{
		{"claude", []string{"claude"}},
		{"codex", []string{"codex"}},
		{"rovodev", []string{"acli"}},
		{"opencode", []string{"opencode"}},
		{"pi", []string{"pi"}},
		{"copilot", []string{"copilot"}},
		{"acpx", []string{"acpx"}},
	}
	for _, alias := range types.ACPAliases() {
		agents = append(agents, doctorAgentCheck{
			name: string(alias.Name),
			binaries: []string{
				alias.DefaultCommandBinary(),
				"acpx",
			},
		})
	}
	return agents
}
