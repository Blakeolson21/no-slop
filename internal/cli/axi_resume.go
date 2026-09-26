package cli

import (
	"fmt"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/resume"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
)

const resumeScopeNote = "Scope check only: base freshness, test-relevant paths, typed test receipts, and preserved adjudications are not checked. In-place resume is not available."

func newAxiResumeCmd() *cobra.Command {
	var runID, head string
	var check bool
	cmd := &cobra.Command{
		Use:   "resume --check --run <id> --head <sha>",
		Short: "Check a proposed repair's candidate ancestry and review-error file scope",
		Long:  "Inspect a parked review from the registered working clone without contacting the daemon.\n" + resumeScopeNote,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !check {
				return emitError(cmd, 1, "in-place resume is not available; use --check for candidate scope inspection only")
			}
			if strings.TrimSpace(runID) == "" || strings.TrimSpace(head) == "" {
				return emitError(cmd, 2, "--run and --head are required")
			}
			scope, err := checkAxiResumeScope(cmd, runID, head)
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			emitDoc(cmd,
				toon.Field{Key: "status", Value: "scope-checked"},
				toon.Field{Key: "run", Value: runID},
				toon.Field{Key: "candidate_sha", Value: scope.CandidateSHA},
				toon.Field{Key: "head_sha", Value: scope.HeadSHA},
				toon.Field{Key: "changed_files", Value: scope.ChangedFiles},
				toon.Field{Key: "resume_available", Value: false},
				toon.Field{Key: "test_receipt_reused", Value: false},
				toon.Field{Key: "note", Value: resumeScopeNote},
			)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "inspect candidate scope only; required in this first slice")
	cmd.Flags().StringVar(&runID, "run", "", "parked run ID")
	cmd.Flags().StringVar(&head, "head", "", "full commit SHA available in the current working clone")
	return cmd
}

func checkAxiResumeScope(cmd *cobra.Command, runID, head string) (resume.Scope, error) {
	p, err := paths.New()
	if err != nil {
		return resume.Scope{}, err
	}
	database, err := db.OpenReadOnly(p.DB())
	if err != nil {
		return resume.Scope{}, fmt.Errorf("open registry read-only: %w", err)
	}
	defer database.Close()
	repo, err := findRepo(database)
	if err != nil {
		return resume.Scope{}, err
	}
	run, err := database.GetRun(runID)
	if err != nil {
		return resume.Scope{}, err
	}
	if run == nil || run.RepoID != repo.ID {
		return resume.Scope{}, fmt.Errorf("run does not belong to the current registered repository")
	}
	steps, err := database.GetStepsByRun(runID)
	if err != nil {
		return resume.Scope{}, err
	}
	var review *db.StepResult
	for _, step := range steps {
		if step.StepName == types.StepReview {
			if review != nil {
				return resume.Scope{}, fmt.Errorf("run has ambiguous review steps")
			}
			review = step
		}
	}
	dir, err := git.FindGitRoot(".")
	if err != nil {
		return resume.Scope{}, err
	}
	return resume.CheckScope(cmd.Context(), dir, run, review, head)
}
