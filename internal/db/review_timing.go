package db

import (
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// ReviewTiming projects the per-run timing record already persisted in
// step_results and agent_invocations. Keeping those normalized records as the
// source of truth avoids a second mutable summary that can drift after recovery.
// TotalMS includes queue and decision waits; ReviewMS/FixMS sum completed agent
// attempt latencies, including failures. Unreported model-only time stays nil.
type ReviewTiming struct {
	StartedAtMS *int64             `json:"started_at_ms"`
	StartedAt   int64              `json:"started_at"`
	Status      types.StepStatus   `json:"status"`
	TotalMS     int64              `json:"total_ms"`
	Complete    bool               `json:"complete"`
	RoundCount  int                `json:"round_count"`
	ReviewMS    int64              `json:"review_ms"`
	FixMS       int64              `json:"fix_ms"`
	Turns       []ReviewTurnTiming `json:"turns"`
}

type ReviewTurnTiming struct {
	Round       int    `json:"round" toon:"round"`
	Purpose     string `json:"purpose" toon:"purpose"`
	LatencyMS   int64  `json:"latency_ms" toon:"latency_ms"`
	ModelMS     *int64 `json:"model_ms" toon:"model_ms"`
	StartedAt   int64  `json:"started_at" toon:"started_at"`
	CompletedAt int64  `json:"completed_at" toon:"completed_at"`
	ExitStatus  string `json:"exit_status" toon:"exit_status"`
}

func (d *DB) GetReviewTiming(runID string, nowUnix int64) (*ReviewTiming, error) {
	return d.getReviewTiming(runID, time.Unix(nowUnix, 0))
}

func (d *DB) GetReviewTimingAt(runID string, now time.Time) (*ReviewTiming, error) {
	return d.getReviewTiming(runID, now)
}

func (d *DB) getReviewTiming(runID string, now time.Time) (*ReviewTiming, error) {
	steps, err := d.GetStepsByRun(runID)
	if err != nil {
		return nil, err
	}
	var review *StepResult
	for _, step := range steps {
		if step.StepName == types.StepReview {
			review = step
			break
		}
	}
	if review == nil {
		return nil, nil
	}
	invocations, err := d.GetAgentInvocationsByRun(runID)
	if err != nil {
		return nil, err
	}
	timing := &ReviewTiming{Status: review.Status, Turns: []ReviewTurnTiming{}}
	startMS := int64(0)
	hasStart := false
	immutableStart := review.FirstStartedAtMS != nil
	if immutableStart {
		startMS = *review.FirstStartedAtMS
		timing.StartedAt = time.UnixMilli(startMS).Unix()
		hasStart = true
	} else if review.StartedAt != nil {
		timing.StartedAt = *review.StartedAt
		startMS = *review.StartedAt * 1000
		if review.StartedAtMS != nil {
			startMS = *review.StartedAtMS
		}
		hasStart = true
	}
	endMS := now.UnixMilli()
	if review.CompletedAt != nil {
		endMS = *review.CompletedAt * 1000
		timing.Complete = true
	}
	if review.CompletedAtMS != nil {
		endMS = *review.CompletedAtMS
		timing.Complete = true
	}
	// A cancelled/crashed run can retain an unfinished step. Its elapsed time
	// stops at terminal run state, rather than growing on every later status.
	if !timing.Complete {
		run, err := d.GetRun(runID)
		if err != nil {
			return nil, err
		}
		if run != nil && (run.Status == types.RunCompleted || run.Status == types.RunFailed || run.Status == types.RunCancelled) {
			if run.TerminalAtMS != nil {
				endMS = *run.TerminalAtMS
			} else {
				endMS = run.UpdatedAt * 1000
			}
			timing.Complete = true
		}
	}

	rounds := make(map[int]bool)
	for _, inv := range invocations {
		if inv.StepName != string(types.StepReview) {
			continue
		}
		if !immutableStart && (!hasStart || inv.StartedAt < timing.StartedAt) {
			timing.StartedAt = inv.StartedAt
			startMS = inv.StartedAt * 1000
			hasStart = true
		}
		turn := ReviewTurnTiming{Round: inv.Round, Purpose: inv.Purpose, LatencyMS: inv.DurationMS, StartedAt: inv.StartedAt, CompletedAt: inv.CompletedAt, ExitStatus: inv.ExitStatus}
		if inv.SubprocessWaitMS != nil {
			ms := agent.ModelTimeMS(inv.DurationMS, *inv.SubprocessWaitMS)
			turn.ModelMS = &ms
		}
		timing.Turns = append(timing.Turns, turn)
		switch inv.Purpose {
		case "review":
			timing.ReviewMS += inv.DurationMS
			rounds[inv.Round] = true
		case "review-fix":
			timing.FixMS += inv.DurationMS
		}
	}
	if !hasStart {
		return nil, nil
	}
	timing.StartedAtMS = &startMS
	timing.TotalMS = max(int64(0), endMS-startMS)
	timing.RoundCount = len(rounds)
	return timing, nil
}
