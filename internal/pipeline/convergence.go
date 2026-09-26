package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/convergence"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
)

// submittedDiffFiles returns the changed-file list of the originally submitted
// diff (base..submitted head). The submitted head, not the live HEAD, is the
// boundary: files a fix round created must count as new surface, not as part
// of what the author submitted. A diff that cannot be resolved reports
// unknown so telemetry omits the count instead of fabricating a zero.
func submittedDiffFiles(ctx context.Context, workDir string, run *db.Run) ([]string, bool) {
	if run == nil || run.BaseSHA == "" {
		return nil, false
	}
	head := run.HeadSHA
	if run.SubmittedHeadSHA != nil && *run.SubmittedHeadSHA != "" {
		head = *run.SubmittedHeadSHA
	}
	if head == "" {
		return nil, false
	}
	out, err := git.Run(ctx, workDir, "diff", "--name-only", "-z", "--no-renames", run.BaseSHA+".."+head)
	if err != nil {
		return nil, false
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, true
}

// evaluateReviewConvergence builds the review step's convergence report from
// its persisted rounds, persists it for status surfaces, and returns it. Every
// failure stops execution: missing history must not bypass a terminal limit.
func (e *Executor) evaluateReviewConvergence(ctx context.Context, stepResultID string, run *db.Run, workDir string) (convergence.Report, error) {
	rounds, err := e.db.GetRoundsByStep(stepResultID)
	if err != nil {
		return convergence.Report{}, fmt.Errorf("load review convergence history: %w", err)
	}
	files, known := submittedDiffFiles(ctx, workDir, run)
	thresholds := convergence.Thresholds{}
	if e.config != nil {
		c := e.config.Review.Convergence
		thresholds = convergence.Thresholds{
			MaxRounds:           c.MaxRounds,
			MaxRecurringRounds:  c.MaxRecurringRounds,
			NonDecreasingRounds: c.NonDecreasingRounds,
			RecurringRounds:     c.RecurringRounds,
			BudgetMS:            int64(c.BudgetMinutes) * 60 * 1000,
		}
	}
	report := convergence.BuildReport(rounds, files, known, thresholds)
	if report.StopReason != "" {
		report.RedesignTicketPath = filepath.Join(e.paths.RunLogDir(run.ID), "redesign.md")
		if err := os.WriteFile(report.RedesignTicketPath, []byte(report.RedesignTicket), 0o644); err != nil {
			return report, fmt.Errorf("write redesign ticket: %w", err)
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		return report, fmt.Errorf("encode review convergence: %w", err)
	}
	if err := e.db.SetStepConvergence(stepResultID, string(data)); err != nil {
		return report, fmt.Errorf("persist review convergence: %w", err)
	}
	return report, nil
}

// ErrNonconverging distinguishes a deliberate terminal limit from step errors.
var ErrNonconverging = errors.New("parked-nonconverging")
