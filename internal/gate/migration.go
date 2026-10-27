package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
)

// MigrationPlan is an advisory, read-only inventory, never authorization to
// delete a gate. Applying a migration needs a fresh check with writers stopped.
// Verified means the source refs and every live run's known commits are intact;
// a missing destination is expected and represented by destination_present=false.
type MigrationPlan struct {
	DryRun         bool              `json:"dry_run"`
	Verified       bool              `json:"verified"`
	RemovalAllowed bool              `json:"removal_allowed"`
	Sources        []MigrationSource `json:"sources"`
	Issues         []string          `json:"issues"`
}

type MigrationSource struct {
	RepoID      string         `json:"repo_id"`
	Source      string         `json:"source"`
	Destination string         `json:"destination"`
	Action      string         `json:"action"`
	Verified    bool           `json:"verified"`
	LiveRuns    int            `json:"live_runs"`
	Refs        []MigrationRef `json:"refs"`
	Issues      []string       `json:"issues"`
}

type MigrationRef struct {
	SourceRef          string `json:"source_ref,omitempty"`
	RunID              string `json:"run_id,omitempty"`
	Role               string `json:"role,omitempty"`
	OID                string `json:"oid"`
	DestinationRef     string `json:"destination_ref"`
	Verified           bool   `json:"verified"`
	DestinationPresent bool   `json:"destination_present"`
}

// PlanMigration reads only registered gate stores under p. It never initializes
// repositories, imports objects, writes refs/config, removes data, or contacts
// an upstream. Orphan directories are reported, never guessed into a group.
func PlanMigration(ctx context.Context, d *db.DB, p *paths.Paths) (*MigrationPlan, error) {
	plan := &MigrationPlan{DryRun: true, Verified: true, Sources: []MigrationSource{}, Issues: []string{}}
	repos, err := d.GetRepos()
	if err != nil {
		return nil, err
	}
	live, err := d.GetActiveRuns()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.Base(repo.ID) != repo.ID || strings.ContainsAny(repo.ID, "/\\") || repo.ID == "." || repo.ID == ".." {
			return nil, fmt.Errorf("invalid registered gate ID")
		}
		source := MigrationSource{RepoID: repo.ID, Source: p.RepoDir(repo.ID), Action: "merge-objects", Verified: true, Refs: []MigrationRef{}, Issues: []string{}}
		known[repo.ID+".git"] = true
		for _, run := range live {
			if run.RepoID == repo.ID {
				source.LiveRuns++
			}
		}
		store, err := UpstreamStoreDir(p, repo.WorkingPath, repo.UpstreamURL)
		if err != nil {
			source.Issues = append(source.Issues, "upstream identity cannot be normalized")
		} else {
			source.Destination = store
		}
		if err := validatePlanGate(ctx, source.Source); err != nil {
			source.Issues = append(source.Issues, "source is missing or is not a bare gate")
		} else {
			// Detect a stale registration instead of planning to mix unrelated objects.
			origin, originErr := git.GetConfiguredRemoteURL(ctx, source.Source, "origin")
			if originErr != nil {
				source.Issues = append(source.Issues, "source upstream is unreadable")
			} else {
				actual, actualErr := UpstreamIdentity(repo.WorkingPath, safeurl.Redact(origin))
				recorded, recordedErr := UpstreamIdentity(repo.WorkingPath, repo.UpstreamURL)
				if actualErr != nil || recordedErr != nil || actual != recorded {
					source.Issues = append(source.Issues, "source upstream disagrees with registration")
				} else {
					// SSH usernames can select repositories and remain part
					// of pool identity; the DB copy may have redacted them.
					store, err = UpstreamStoreDir(p, repo.WorkingPath, origin)
					if err != nil {
						source.Issues = append(source.Issues, "source upstream identity cannot be normalized")
					} else {
						source.Destination = store
					}
				}
			}
			if configured, err := git.RunBare(ctx, source.Source, "config", "--get", objectStoreKey); err == nil {
				if configured == store {
					source.Action = "already-shared"
				} else {
					source.Issues = append(source.Issues, "gate borrows a different upstream store")
				}
			}
			// Use explicit --git-dir through RunBare: malformed/missing stores may
			// never fall back to discovering an enclosing developer checkout.
			refs, err := git.RunBare(ctx, source.Source, "for-each-ref", "--format=%(objectname) %(refname)")
			if err != nil {
				source.Issues = append(source.Issues, "source refs cannot be read")
			} else {
				for _, line := range strings.Split(refs, "\n") {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						continue
					}
					source.Refs = append(source.Refs, MigrationRef{SourceRef: fields[1], OID: fields[0], DestinationRef: "refs/lanes/" + repo.ID + "/" + strings.TrimPrefix(fields[1], "refs/")})
				}
			}
			for _, run := range live {
				if run.RepoID != repo.ID {
					continue
				}
				if _, err := git.RunBare(ctx, source.Source, "check-ref-format", "refs/runs/"+run.ID+"/head"); err != nil {
					source.Issues = append(source.Issues, "invalid live run namespace")
					continue
				}
				add := func(role, oid string) {
					source.Refs = append(source.Refs, MigrationRef{RunID: run.ID, Role: role, OID: oid, DestinationRef: "refs/runs/" + run.ID + "/" + oid})
				}
				add("head", run.HeadSHA)
				if !nullObjectID(run.BaseSHA) {
					add("base", run.BaseSHA)
				}
				for _, v := range []struct {
					role  string
					value *string
				}{{"submitted", run.SubmittedHeadSHA}, {"approved", run.ReviewApprovedHeadSHA}, {"last-pushed", run.LastPushedSHA}} {
					if v.value != nil && *v.value != "" {
						add(v.role, *v.value)
					}
				}
				wt := p.WorktreeDir(repo.ID, run.ID)
				if _, err := os.Stat(wt); err == nil {
					// An executing fix may have committed ahead of runs.head_sha.
					common, commonErr := git.Run(ctx, wt, "rev-parse", "--git-common-dir")
					if !filepath.IsAbs(common) {
						common = filepath.Join(wt, common)
					}
					if commonErr != nil || canonicalWorkingRemotePath(common) != canonicalWorkingRemotePath(source.Source) {
						source.Issues = append(source.Issues, "live worktree does not belong to its gate")
					} else if head, err := git.HeadSHA(ctx, wt); err != nil {
						source.Issues = append(source.Issues, "live worktree head is unreadable")
					} else {
						add("worktree-head", head)
					}
				} else if !os.IsNotExist(err) {
					source.Issues = append(source.Issues, "live worktree cannot be inspected")
				}
				steps, err := d.GetStepsByRun(run.ID)
				if err != nil {
					return nil, err
				}
				for _, step := range steps {
					rounds, err := d.GetRoundsByStep(step.ID)
					if err != nil {
						return nil, err
					}
					for _, round := range rounds {
						for _, v := range []struct {
							role  string
							value *string
						}{{"reviewed-round", round.ReviewedHeadSHA}, {"starting-round", round.StartingHeadSHA}, {"trusted-config", round.TrustedConfigSHA}} {
							if v.value != nil && *v.value != "" {
								add(fmt.Sprintf("%s/%s/%d", v.role, step.ID, round.Round), *v.value)
							}
						}
					}
				}
			}
			for i := range source.Refs {
				ref := &source.Refs[i]
				if validObjectID(ref.OID) {
					spec := ref.OID
					if ref.RunID != "" {
						spec += "^{commit}"
					}
					_, err := git.RunBare(ctx, source.Source, "cat-file", "-e", spec)
					ref.Verified = err == nil
					if store != "" {
						if got, err := git.RunBare(ctx, store, "rev-parse", "--verify", ref.DestinationRef); err == nil && got == ref.OID {
							ref.DestinationPresent = true
						}
					}
				}
				if !ref.Verified {
					source.Issues = append(source.Issues, "required ref or live run commit is unavailable")
				}
			}
			// Connectivity includes trees/blobs, not merely the existence of each tip.
			if _, err := git.RunBare(ctx, source.Source, "fsck", "--connectivity-only", "--no-dangling", "--no-reflogs"); err != nil {
				source.Issues = append(source.Issues, "source object connectivity verification failed")
			}
			var liveObjects []string
			for _, ref := range source.Refs {
				if ref.Verified && ref.RunID != "" {
					liveObjects = append(liveObjects, ref.OID)
				}
			}
			for start := 0; start < len(liveObjects); start += 128 {
				args := []string{"fsck", "--connectivity-only", "--no-dangling", "--no-reflogs"}
				args = append(args, liveObjects[start:min(start+128, len(liveObjects))]...)
				if _, err := git.RunBare(ctx, source.Source, args...); err != nil {
					source.Issues = append(source.Issues, "live run object connectivity verification failed")
				}
			}
		}
		source.Verified = len(source.Issues) == 0
		if !source.Verified {
			plan.Verified = false
		}
		plan.Sources = append(plan.Sources, source)
	}
	entries, err := os.ReadDir(p.ReposDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".git") && !known[entry.Name()] {
			plan.Issues = append(plan.Issues, "unregistered gate directory: "+entry.Name())
			plan.Verified = false
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := d.GetActiveRuns()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(live, after) {
		plan.Verified = false
		plan.Issues = append(plan.Issues, "live runs changed during inspection; repeat with writers stopped before applying a migration")
	}
	return plan, nil
}

func validatePlanGate(ctx context.Context, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("gate is not a directory")
	}
	bare, err := git.RunBare(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil || bare != "true" {
		return fmt.Errorf("gate is not bare")
	}
	return nil
}
