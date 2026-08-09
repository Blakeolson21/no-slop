package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/convergence"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
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
// failure is soft: convergence is observability plus an advisory guard, so a
// telemetry error must never fail the run.
func (e *Executor) evaluateReviewConvergence(ctx context.Context, stepResultID string, run *db.Run, workDir string) convergence.Report {
	rounds, err := e.db.GetRoundsByStep(stepResultID)
	if err != nil {
		slog.Warn("failed to load review rounds for convergence report", "error", err)
		return convergence.Report{}
	}
	files, known := submittedDiffFiles(ctx, workDir, run)
	thresholds := convergence.Thresholds{}
	if e.config != nil {
		c := e.config.Review.Convergence
		thresholds = convergence.Thresholds{
			NonDecreasingRounds: c.NonDecreasingRounds,
			RecurringRounds:     c.RecurringRounds,
			BudgetMS:            int64(c.BudgetMinutes) * 60 * 1000,
		}
	}
	report := convergence.BuildReport(rounds, files, known, thresholds)
	if data, err := json.Marshal(report); err == nil {
		if dbErr := e.db.SetStepConvergence(stepResultID, string(data)); dbErr != nil {
			slog.Warn("failed to persist convergence report", "error", dbErr)
		}
	}
	return report
}
