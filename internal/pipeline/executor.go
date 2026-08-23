package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/gateguidance"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
	"github.com/Blakeolson21/no-slop/internal/telemetry"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// EventFunc is called when a pipeline event occurs, for streaming to subscribers.
type EventFunc func(ipc.Event)

const (
	defaultGateReconcileInterval = 2 * time.Minute
	defaultGateReconcileTimeout  = 30 * time.Second
)

type approvalResponse struct {
	action        types.ApprovalAction
	findingIDs    []string
	instructions  map[string]string
	addedFindings []types.Finding
}

// Executor runs pipeline steps sequentially and coordinates approval interactions.
type Executor struct {
	db     *db.DB
	paths  *paths.Paths
	config *config.Config
	agent  agent.Agent
	steps  []Step
	skips  map[types.StepName]bool

	onEvent EventFunc

	// sessions manages this run's durable review-loop agent sessions; shared
	// carries run-scoped step-to-step results. Both are created per Execute.
	sessions *RunSessions
	shared   *RunShared
	workDir  string

	mu          sync.Mutex
	approvalCh  chan approvalResponse // buffered channel for approval responses
	waiting     bool                  // true when blocked on approval
	waitingStep types.StepName        // which step is currently awaiting approval

	gateReconcileInterval time.Duration
	gateReconcileTimeout  time.Duration
	onPRMerged            func(context.Context, string)
}

// SetOnPRMerged registers a best-effort hook invoked after a merged PR state
// is persisted. The pipeline never fails the run if the hook errors.
func (e *Executor) SetOnPRMerged(fn func(context.Context, string)) {
	if e == nil {
		return
	}
	e.onPRMerged = fn
}

// SetSkippedSteps configures steps that should be marked skipped without running.
func (e *Executor) SetSkippedSteps(steps []types.StepName) {
	if len(steps) == 0 {
		e.skips = nil
		return
	}
	e.skips = make(map[types.StepName]bool, len(steps))
	for _, step := range steps {
		e.skips[step] = true
	}
}

// NewExecutor creates a pipeline executor.
func NewExecutor(database *db.DB, p *paths.Paths, cfg *config.Config, ag agent.Agent, steps []Step, onEvent EventFunc) *Executor {
	if onEvent == nil {
		onEvent = func(ipc.Event) {}
	}
	return &Executor{
		db:                    database,
		paths:                 p,
		config:                cfg,
		agent:                 ag,
		steps:                 steps,
		onEvent:               onEvent,
		approvalCh:            make(chan approvalResponse, 1),
		gateReconcileInterval: defaultGateReconcileInterval,
		gateReconcileTimeout:  defaultGateReconcileTimeout,
	}
}

// runEvidenceDir resolves where this run's test evidence is written. The
// executor is the single owner of that answer for the pipeline: steps read it
// from StepContext rather than recomputing a path, which is what let the
// steering preamble and the test step drift apart while both hardcoded the
// system temp directory.
func (e *Executor) runEvidenceDir(runID string) string {
	if e.paths == nil {
		return ""
	}
	configured := ""
	if e.config != nil {
		configured = e.config.Test.Evidence.LocalRoot
	}
	return e.paths.RunEvidenceDir(configured, runID)
}

// SetGateReconcileTimings overrides the interval between approval-gate
// reconciliation checks and the deadline for each check. It is primarily used
// by deterministic tests and specialized embeddings; non-positive values keep
// the production defaults.
func (e *Executor) SetGateReconcileTimings(interval, timeout time.Duration) {
	if interval > 0 {
		e.gateReconcileInterval = interval
	}
	if timeout > 0 {
		e.gateReconcileTimeout = timeout
	}
}

// Respond sends a user approval action to the currently waiting step.
// The step parameter must match the step currently awaiting approval.
// Returns an error if no step is awaiting approval or if the step name doesn't match.
func (e *Executor) Respond(step types.StepName, action types.ApprovalAction, findingIDs []string) error {
	return e.RespondWithOverrides(step, action, findingIDs, nil, nil)
}

// RespondWithOverrides is like Respond but also carries per-finding user
// instructions and user-authored findings. Both are merged into the round's
// findings on a fix action before the fix agent runs.
func (e *Executor) RespondWithOverrides(step types.StepName, action types.ApprovalAction, findingIDs []string, instructions map[string]string, addedFindings []types.Finding) error {
	e.mu.Lock()
	if !e.waiting {
		e.mu.Unlock()
		return fmt.Errorf("no step awaiting approval")
	}
	if step != e.waitingStep {
		e.mu.Unlock()
		return fmt.Errorf("step mismatch: responding to %q but %q is awaiting approval", step, e.waitingStep)
	}
	e.waiting = false
	e.mu.Unlock()

	e.approvalCh <- approvalResponse{
		action:        action,
		findingIDs:    findingIDs,
		instructions:  instructions,
		addedFindings: addedFindings,
	}
	return nil
}

// Execute runs the pipeline steps sequentially for a given run.
// The workDir is the directory where steps execute (typically a git worktree).
// If the context is cancelled with a cause (via context.WithCancelCause),
// the cause message is preserved as the run's error in the DB.
func (e *Executor) Execute(ctx context.Context, run *db.Run, repo *db.Repo, workDir string) error {
	e.workDir = workDir
	// Mark run as running. Route write failures through failRun so the
	// in-memory lifecycle and subscriber stream still become terminal instead
	// of leaving a silent pending run.
	if err := e.db.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		return e.failRun(run, repo, fmt.Errorf("update run status: %w", err))
	}
	run.Status = types.RunRunning
	e.emitRunEvent(ipc.EventRunUpdated, run, repo)

	// Create log directory for this run
	logDir := e.paths.RunLogDir(run.ID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return e.failRun(run, repo, fmt.Errorf("create log dir: %w", err))
	}

	e.initializeRunScopes(run.ID)

	// Create step result records in DB
	stepRecords := make(map[types.StepName]*db.StepResult)
	for _, step := range e.steps {
		sr, err := e.db.InsertStepResult(run.ID, step.Name())
		if err != nil {
			return e.failRun(run, repo, fmt.Errorf("insert step result: %w", err))
		}
		stepRecords[step.Name()] = sr
	}

	// Execute steps sequentially. A late repair may send the same run back
	// through validation before any new head is published.
	for i := 0; i < len(e.steps); i++ {
		step := e.steps[i]
		if ctx.Err() != nil {
			return e.failRun(run, repo, context.Cause(ctx))
		}

		sr := stepRecords[step.Name()]
		sr, err := e.dispatchableStepResult(sr.ID, step.Name())
		if err != nil {
			return e.failRun(run, repo, fmt.Errorf("restore step %s result: %w", step.Name(), err), ctx)
		}
		stepRecords[step.Name()] = sr
		if sr.Status == types.StepStatusSkipped {
			continue
		}
		if e.skips[step.Name()] {
			if err := e.db.CompleteStepWithStatus(sr.ID, types.StepStatusSkipped, 0, 0, ""); err != nil {
				return e.failRun(run, repo, fmt.Errorf("skip step %s: %w", step.Name(), err), ctx)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, step.Name(), string(types.StepStatusSkipped), "", "", nil)
			continue
		}
		state, err := e.durableExecutionState(sr.ID)
		if err != nil {
			return e.failRun(run, repo, fmt.Errorf("restore step %s execution state: %w", step.Name(), err), ctx)
		}
		skipRemaining, restartFrom, err := e.executeStep(ctx, step, sr, run, repo, workDir, logDir, state)
		if err != nil {
			return e.failRun(run, repo, err, ctx)
		}
		if skipRemaining {
			// Mark all subsequent steps as skipped
			for _, remaining := range e.steps[i+1:] {
				rsr := stepRecords[remaining.Name()]
				if dbErr := e.db.CompleteStepWithStatus(rsr.ID, types.StepStatusSkipped, 0, 0, ""); dbErr != nil {
					slog.Warn("failed to finalize skipped step", "step", remaining.Name(), "error", dbErr)
				}
				e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, remaining.Name(), string(types.StepStatusSkipped), "", "", nil)
			}
			break
		}
		if restartFrom != "" {
			restartIndex, err := e.prepareRestart(run, repo, restartFrom, i)
			if err != nil {
				return e.failRun(run, repo, restartStepError(step.Name(), restartFrom, err), ctx)
			}
			i = restartIndex - 1
		}
	}

	// Mark run as completed. A failure here must emit a terminal failure rather
	// than leaving a silent running row after every step has finished.
	if err := e.completeRun(run, repo); err != nil {
		return e.failRun(run, repo, fmt.Errorf("update run status: %w", err))
	}
	return nil
}

func (e *Executor) stepIndex(name types.StepName) (int, error) {
	for index, step := range e.steps {
		if step.Name() == name {
			return index, nil
		}
	}
	return 0, fmt.Errorf("step %s is not in the pipeline", name)
}

func (e *Executor) prepareRestart(run *db.Run, repo *db.Repo, name types.StepName, currentIndex int) (int, error) {
	index, err := e.stepIndex(name)
	if err != nil {
		return 0, err
	}
	if index >= currentIndex {
		return 0, fmt.Errorf("restart boundary is not before the current step")
	}
	if err := e.db.ResetStepsFrom(run.ID, e.steps[index].Name().Order()); err != nil {
		return 0, err
	}
	e.onEvent(ipc.Event{Type: ipc.EventStepsReset, RunID: run.ID, RepoID: repo.ID})
	return index, nil
}

func (e *Executor) initializeRunScopes(runID string) {
	sessionsEnabled := e.config != nil && e.config.SessionReuse && e.agent != nil
	e.sessions = NewRunSessions(e.db, runID, e.agent, sessionsEnabled)
	e.shared = &RunShared{}
}

func (e *Executor) executionStateForStep(step Step, sr *db.StepResult) (stepExecutionState, *db.StepResult, error) {
	fresh, err := e.db.GetStepResult(sr.ID)
	if err != nil {
		return stepExecutionState{}, nil, err
	}
	if fresh == nil {
		return stepExecutionState{}, nil, fmt.Errorf("step result %s not found", sr.ID)
	}
	rounds, err := e.db.GetRoundsByStep(sr.ID)
	if err != nil {
		return stepExecutionState{}, nil, err
	}
	state := stepExecutionState{}
	if len(rounds) > 0 {
		latest := rounds[len(rounds)-1]
		state.roundNum = latest.Round
		state.currentRoundID = latest.ID
		for _, round := range rounds {
			if round.SelectionSource != nil && *round.SelectionSource == db.RoundSelectionSourceAutoFix {
				state.autoFixAttempts++
			}
		}
	}
	if findingsMayBeScopeLimited(step) && fresh.FindingsJSON != nil {
		state.carriedFindings = *fresh.FindingsJSON
	}
	return state, fresh, nil
}

func (e *Executor) restartIndexForStaleRequiredGates(run *db.Run) (int, error) {
	steps, err := e.db.GetStepsByRun(run.ID)
	if err != nil {
		return -1, fmt.Errorf("load required gate certifications: %w", err)
	}
	required := map[types.StepName]bool{
		types.StepReview:   true,
		types.StepTest:     true,
		types.StepDocument: true,
	}
	earliest := -1
	for index, step := range steps {
		if !required[step.StepName] || step.Status != types.StepStatusCompleted {
			continue
		}
		if step.CertifiedHeadSHA != nil && *step.CertifiedHeadSHA == run.HeadSHA {
			continue
		}
		if earliest < 0 || index < earliest {
			earliest = index
		}
	}
	if earliest < 0 {
		return -1, nil
	}
	if err := e.db.ResetStepsFromOrder(run.ID, e.steps[earliest].Name().Order()); err != nil {
		return -1, fmt.Errorf("invalidate stale required gates: %w", err)
	}
	slog.Info("pipeline head changed; rerunning required gates", "run", run.ID, "head", run.HeadSHA, "from", e.steps[earliest].Name())
	return earliest, nil
}

type stepExecutionState struct {
	fixing           bool
	previousFindings string
	roundNum         int
	autoFixAttempts  int
	executionMS      int64
	currentRoundID   string
	carriedFindings  string
}

func (e *Executor) durableExecutionState(stepResultID string) (stepExecutionState, error) {
	rounds, err := e.db.GetRoundsByStep(stepResultID)
	if err != nil {
		return stepExecutionState{}, err
	}
	state := stepExecutionState{}
	for _, round := range rounds {
		state.roundNum = max(state.roundNum, round.Round)
		if round.SelectionSource != nil && *round.SelectionSource == db.RoundSelectionSourceAutoFix {
			state.autoFixAttempts++
		}
	}
	return state, nil
}

func (e *Executor) dispatchableStepResult(stepResultID string, stepName types.StepName) (*db.StepResult, error) {
	result, err := e.db.GetStepResult(stepResultID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("missing step result")
	}
	if result.StepName != stepName {
		return nil, fmt.Errorf("step result is for %s", result.StepName)
	}
	if result.Status != types.StepStatusPending && result.Status != types.StepStatusSkipped {
		return nil, fmt.Errorf("step result is %s", result.Status)
	}
	return result, nil
}

type recoveredGate struct {
	index           int
	step            Step
	stepResult      *db.StepResult
	findings        string
	round           int
	autoFixes       int
	lastRoundID     string
	reviewedHeadSHA string
}

func ValidateRecoveredRun(database *db.DB, run *db.Run, steps []Step) error {
	if run == nil || run.Status != types.RunRunning || run.AwaitingAgentSince == nil {
		return fmt.Errorf("run is not a recoverable parked run")
	}
	_, err := (&Executor{db: database, steps: steps}).recoveredGate(run.ID)
	return err
}

// Resume restores a run that was durably parked at an approval gate when the
// daemon stopped. It only accepts a fully recorded gate and otherwise returns
// an error so startup recovery can fail the run rather than guessing.
func (e *Executor) Resume(ctx context.Context, run *db.Run, repo *db.Repo, workDir string) error {
	e.workDir = workDir
	if repo == nil {
		return fmt.Errorf("recovered run has no repository")
	}
	if err := ValidateRecoveredRun(e.db, run, e.steps); err != nil {
		return err
	}
	gate, err := e.recoveredGate(run.ID)
	if err != nil {
		return err
	}
	logDir := e.paths.RunLogDir(run.ID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return e.failRun(run, repo, fmt.Errorf("create log dir: %w", err))
	}
	e.initializeRunScopes(run.ID)

	parkStart := time.Unix(*run.AwaitingAgentSince, 0)
	duration := recoveredStepDuration(gate.stepResult)
	completeRecoveredGate := func() error {
		if gate.step.Name() == types.StepReview {
			if gate.reviewedHeadSHA == "" {
				return fmt.Errorf("recovered review has no durable reviewed head candidate")
			}
			if err := e.db.CompleteReviewStep(gate.stepResult.ID, run.ID, gate.reviewedHeadSHA, recoveredExitCode(gate.stepResult), duration, recoveredLogPath(gate.stepResult)); err != nil {
				return err
			}
			reviewedHead := gate.reviewedHeadSHA
			run.ReviewApprovedHeadSHA = &reviewedHead
			ClearUncertifiedPipelineRangeIfCertified(ctx, e.db, repo.ID, run.Branch, reviewedHead, workDir)
			return nil
		}
		return e.db.CompleteStepWithStatusAtHead(gate.stepResult.ID, types.StepStatusCompleted, run.HeadSHA, recoveredExitCode(gate.stepResult), duration, recoveredLogPath(gate.stepResult))
	}
	completeReconciledGate := func() error {
		if err := completeRecoveredGate(); err != nil {
			return e.failRun(run, repo, fmt.Errorf("complete reconciled step %s: %w", gate.step.Name(), err), ctx)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusCompleted), "", "", &duration)
		return e.executeRecoveredRemainder(ctx, run, repo, workDir, logDir, gate.index+1)
	}
	reconcileCtx := &StepContext{
		Ctx:      ctx,
		Run:      run,
		Repo:     repo,
		WorkDir:  workDir,
		Config:   e.config,
		DB:       e.db,
		Agent:    e.agent,
		Sessions: e.sessions,
		Shared:   e.shared,
		Log: func(message string) {
			slog.Info("recovered approval gate reconciliation", "run_id", run.ID, "step", gate.step.Name(), "message", message)
		},
		LogChunk:   func(string) {},
		LogFile:    func(string) {},
		OnPRMerged: e.onPRMerged,
	}
	if reconciled, reconcileErr := e.reconcileApprovalGate(ctx, gate.step, reconcileCtx); reconciled {
		if dbErr := e.db.CompleteRunAwaitingAgent(run.ID, time.Since(parkStart).Milliseconds()); dbErr != nil {
			return e.failRun(run, repo, fmt.Errorf("complete reconciled awaiting-agent state: %w", dbErr), ctx)
		}
		return completeReconciledGate()
	} else if reconcileErr != nil && ctx.Err() == nil {
		if errors.Is(reconcileErr, ErrFatalGateReconciliation) {
			if dbErr := e.db.CompleteRunAwaitingAgent(run.ID, time.Since(parkStart).Milliseconds()); dbErr != nil {
				return e.failRun(run, repo, fmt.Errorf("complete fatal reconciliation awaiting-agent state: %w", dbErr), ctx)
			}
			if dbErr := e.db.FailStep(gate.stepResult.ID, reconcileErr.Error(), duration); dbErr != nil {
				slog.Warn("failed to mark recovered step as failed in db", "step", gate.step.Name(), "error", dbErr)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusFailed), "", reconcileErr.Error(), &duration)
			return e.failRun(run, repo, fmt.Errorf("step %s: reconcile approval gate: %w", gate.step.Name(), reconcileErr), ctx)
		}
		slog.Warn("could not reconcile recovered approval gate; preserving it", "run_id", run.ID, "step", gate.step.Name(), "error", reconcileErr)
	}

	e.mu.Lock()
	e.waiting = true
	e.waitingStep = gate.step.Name()
	e.mu.Unlock()
	e.emitStepEventWithFindingsAndError(
		ipc.EventStepCompleted,
		run,
		repo,
		gate.step.Name(),
		string(gate.stepResult.Status),
		gate.findings,
		"",
		gate.stepResult.DurationMS,
	)

	response, reconciled, err := e.waitForApprovalOrReconcile(ctx, gate.step, reconcileCtx, false)
	if dbErr := e.db.CompleteRunAwaitingAgent(run.ID, time.Since(parkStart).Milliseconds()); dbErr != nil {
		slog.Warn("failed to complete awaiting-agent state in db", "step", gate.step.Name(), "run", run.ID, "error", dbErr)
	}
	if err != nil {
		if dbErr := e.db.FailStep(gate.stepResult.ID, err.Error(), duration); dbErr != nil {
			slog.Warn("failed to mark recovered step as failed in db", "step", gate.step.Name(), "error", dbErr)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusFailed), "", err.Error(), &duration)
		return e.failRun(run, repo, fmt.Errorf("step %s: waiting for approval: %w", gate.step.Name(), err), ctx)
	}
	if reconciled {
		return completeReconciledGate()
	}

	approvalFields := telemetry.Fields{
		"step":       string(gate.step.Name()),
		"action":     string(response.action),
		"fix_review": gate.stepResult.Status == types.StepStatusFixReview,
	}
	if agentName := e.telemetryAgentName(); agentName != "" {
		approvalFields["agent"] = agentName
	}
	if selectedCount := selectedFindingCount(gate.findings, response.findingIDs); selectedCount > 0 {
		approvalFields["selected_findings_count"] = selectedCount
	}
	telemetry.Track("approval", approvalFields)
	switch response.action {
	case types.ActionApprove:
		if err := completeRecoveredGate(); err != nil {
			return e.failRun(run, repo, fmt.Errorf("complete recovered step %s: %w", gate.step.Name(), err), ctx)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusCompleted), "", "", &duration)
		return e.executeRecoveredRemainder(ctx, run, repo, workDir, logDir, gate.index+1)
	case types.ActionSkip:
		if err := e.db.CompleteStepWithStatus(gate.stepResult.ID, types.StepStatusSkipped, recoveredExitCode(gate.stepResult), duration, recoveredLogPath(gate.stepResult)); err != nil {
			return e.failRun(run, repo, fmt.Errorf("skip recovered step %s: %w", gate.step.Name(), err), ctx)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusSkipped), "", "", &duration)
		return e.executeRecoveredRemainder(ctx, run, repo, workDir, logDir, gate.index+1)
	case types.ActionAbort:
		if dbErr := e.db.FailStep(gate.stepResult.ID, "aborted by user", duration); dbErr != nil {
			slog.Warn("failed to mark recovered step as aborted", "step", gate.step.Name(), "error", dbErr)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusFailed), "", "aborted by user", &duration)
		return e.failRun(run, repo, fmt.Errorf("step %s: aborted by user", gate.step.Name()), ctx)
	case types.ActionFix:
		telemetry.Track("fix", e.fixTelemetryFields("user", gate.step.Name(), selectedFindingCount(gate.findings, response.findingIDs), 0))
		selected := filterFindingsJSON(gate.findings, response.findingIDs)
		merged := mergeUserOverridesJSON(selected, response.instructions, response.addedFindings)
		if err := e.persistUserFixDecision(gate.lastRoundID, response.findingIDs, selected, merged); err != nil {
			if findingsMayBeScopeLimited(gate.step) {
				return e.failRun(run, repo, fmt.Errorf("record recovered %s user decision: %w", gate.step.Name(), err), ctx)
			}
			slog.Warn("failed to record recovered user decision", "step", gate.step.Name(), "round", gate.round, "error", err)
		}
		if dbErr := e.db.UpdateStepStatus(gate.stepResult.ID, types.StepStatusFixing); dbErr != nil {
			return e.failRun(run, repo, fmt.Errorf("mark recovered step %s fixing: %w", gate.step.Name(), dbErr), ctx)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, gate.step.Name(), string(types.StepStatusFixing), "", "", nil)
		carried := ""
		if findingsMayBeScopeLimited(gate.step) {
			carried = excludeFindingsJSON(gate.findings, response.findingIDs)
		}
		skipRemaining, restartFrom, err := e.executeStep(ctx, gate.step, gate.stepResult, run, repo, workDir, logDir, stepExecutionState{
			fixing:           true,
			previousFindings: merged,
			roundNum:         gate.round,
			autoFixAttempts:  gate.autoFixes,
			executionMS:      duration,
			currentRoundID:   gate.lastRoundID,
			carriedFindings:  carried,
		})
		if err != nil {
			return e.failRun(run, repo, err, ctx)
		}
		if skipRemaining {
			return e.skipRecoveredRemainder(run, repo, gate.index+1)
		}
		if restartFrom != "" {
			restartIndex, indexErr := e.prepareRestart(run, repo, restartFrom, gate.index)
			if indexErr != nil {
				return e.failRun(run, repo, restartStepError(gate.step.Name(), restartFrom, indexErr), ctx)
			}
			return e.executeRecoveredRemainder(ctx, run, repo, workDir, logDir, restartIndex)
		}
		return e.executeRecoveredRemainder(ctx, run, repo, workDir, logDir, gate.index+1)
	default:
		return e.failRun(run, repo, fmt.Errorf("step %s: unsupported approval action %q", gate.step.Name(), response.action), ctx)
	}
}

func (e *Executor) recoveredGate(runID string) (*recoveredGate, error) {
	results, err := e.db.GetStepsByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("get recovered steps: %w", err)
	}
	if len(results) != len(e.steps) {
		return nil, fmt.Errorf("recovered run has %d step records for %d steps", len(results), len(e.steps))
	}

	var gate *recoveredGate
	for index, result := range results {
		if result.StepName != e.steps[index].Name() {
			return nil, fmt.Errorf("recovered step %d is %q, want %q", index, result.StepName, e.steps[index].Name())
		}
		if result.Status == types.StepStatusAwaitingApproval || result.Status == types.StepStatusFixReview {
			if gate != nil || result.FindingsJSON == nil || result.StartedAt == nil || result.DurationMS == nil || result.AgentPID != nil {
				return nil, fmt.Errorf("recovered approval gate is incomplete")
			}
			rounds, err := e.db.GetRoundsByStep(result.ID)
			if err != nil || len(rounds) == 0 {
				return nil, fmt.Errorf("recovered approval gate has no complete round")
			}
			latest := rounds[len(rounds)-1]
			if latest.FindingsJSON == nil || *latest.FindingsJSON != *result.FindingsJSON {
				return nil, fmt.Errorf("recovered approval gate findings are incomplete")
			}
			autoFixes := 0
			for _, round := range rounds {
				if round.SelectionSource != nil && *round.SelectionSource == db.RoundSelectionSourceAutoFix {
					autoFixes++
				}
			}
			gate = &recoveredGate{
				index:       index,
				step:        e.steps[index],
				stepResult:  result,
				findings:    *result.FindingsJSON,
				round:       latest.Round,
				autoFixes:   autoFixes,
				lastRoundID: latest.ID,
			}
			if latest.ReviewedHeadSHA != nil {
				gate.reviewedHeadSHA = *latest.ReviewedHeadSHA
			}
			continue
		}
		if gate == nil {
			if result.Status != types.StepStatusCompleted && result.Status != types.StepStatusSkipped {
				return nil, fmt.Errorf("recovered step %s is %s before approval gate", result.StepName, result.Status)
			}
			continue
		}
		if result.Status != types.StepStatusPending && result.Status != types.StepStatusSkipped {
			return nil, fmt.Errorf("recovered step %s is %s after approval gate", result.StepName, result.Status)
		}
	}
	if gate == nil {
		return nil, fmt.Errorf("recovered run has no approval gate")
	}
	return gate, nil
}

func (e *Executor) executeRecoveredRemainder(ctx context.Context, run *db.Run, repo *db.Repo, workDir, logDir string, start int) error {
	results, err := e.db.GetStepsByRun(run.ID)
	if err != nil {
		return e.failRun(run, repo, fmt.Errorf("get recovered steps: %w", err), ctx)
	}
	for index := start; index < len(e.steps); index++ {
		if ctx.Err() != nil {
			return e.failRun(run, repo, context.Cause(ctx), ctx)
		}
		if index >= len(results) || results[index].StepName != e.steps[index].Name() {
			return e.failRun(run, repo, fmt.Errorf("recovered step plan changed at %d", index), ctx)
		}
		result, resultErr := e.dispatchableStepResult(results[index].ID, e.steps[index].Name())
		if resultErr != nil {
			return e.failRun(run, repo, fmt.Errorf("restore recovered step %s result: %w", e.steps[index].Name(), resultErr), ctx)
		}
		results[index] = result
		if result.Status == types.StepStatusSkipped {
			continue
		}
		state, stateErr := e.durableExecutionState(result.ID)
		if stateErr != nil {
			return e.failRun(run, repo, fmt.Errorf("restore step %s execution state: %w", e.steps[index].Name(), stateErr), ctx)
		}
		skipRemaining, restartFrom, err := e.executeStep(ctx, e.steps[index], result, run, repo, workDir, logDir, state)
		if err != nil {
			return e.failRun(run, repo, err, ctx)
		}
		if skipRemaining {
			return e.skipRecoveredRemainder(run, repo, index+1)
		}
		if restartFrom != "" {
			restartIndex, indexErr := e.prepareRestart(run, repo, restartFrom, index)
			if indexErr != nil {
				return e.failRun(run, repo, restartStepError(e.steps[index].Name(), restartFrom, indexErr), ctx)
			}
			index = restartIndex - 1
		}
	}
	if err := e.completeRun(run, repo); err != nil {
		return e.failRun(run, repo, fmt.Errorf("complete recovered run: %w", err), ctx)
	}
	return nil
}

func restartStepError(stepName, restartFrom types.StepName, err error) error {
	return fmt.Errorf("step %s requested restart from %s: %w", stepName, restartFrom, err)
}

func (e *Executor) skipRecoveredRemainder(run *db.Run, repo *db.Repo, start int) error {
	results, err := e.db.GetStepsByRun(run.ID)
	if err != nil {
		return e.failRun(run, repo, fmt.Errorf("get recovered steps: %w", err))
	}
	for index := start; index < len(e.steps); index++ {
		if index >= len(results) || results[index].StepName != e.steps[index].Name() || results[index].Status != types.StepStatusPending {
			return e.failRun(run, repo, fmt.Errorf("recovered step plan changed at %d", index))
		}
		if err := e.db.CompleteStepWithStatus(results[index].ID, types.StepStatusSkipped, 0, 0, ""); err != nil {
			return e.failRun(run, repo, fmt.Errorf("skip recovered step %s: %w", e.steps[index].Name(), err))
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, e.steps[index].Name(), string(types.StepStatusSkipped), "", "", nil)
	}
	if err := e.completeRun(run, repo); err != nil {
		return e.failRun(run, repo, fmt.Errorf("complete recovered run: %w", err))
	}
	return nil
}

func recoveredStepDuration(step *db.StepResult) int64 {
	if step != nil && step.DurationMS != nil {
		return *step.DurationMS
	}
	return 0
}

func recoveredExitCode(step *db.StepResult) int {
	if step != nil && step.ExitCode != nil {
		return *step.ExitCode
	}
	return 0
}

func recoveredLogPath(step *db.StepResult) string {
	if step != nil && step.LogPath != nil {
		return *step.LogPath
	}
	return ""
}

// executeStep runs a single step with approval coordination.
// Returns whether to skip the remainder, an optional earlier restart step,
// and any execution error.
func (e *Executor) executeStep(ctx context.Context, step Step, sr *db.StepResult, run *db.Run, repo *db.Repo, workDir, logDir string, state stepExecutionState) (bool, types.StepName, error) {
	stepName := step.Name()
	logPath := filepath.Join(logDir, string(stepName)+".log")
	finalExitCode := 0
	autoFixLimit := 0
	if e.config != nil {
		autoFixLimit = e.config.AutoFixLimit(stepName)
	}

	// Mark step as running
	if err := e.db.StartStepWithAutoFixLimit(sr.ID, autoFixLimit); err != nil {
		return false, "", fmt.Errorf("start step %s: %w", stepName, err)
	}
	e.emitStepEvent(ipc.EventStepStarted, run, repo, stepName, string(types.StepStatusRunning))

	// Track execution-only time, excluding approval wait periods.
	phaseStart := time.Now()
	executionMS := state.executionMS
	var durationOverrideMS int64 // sum of step-reported overrides (demo mode)

	// Open log file for persistent step logging
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, "", fmt.Errorf("create step log file %s: %w", stepName, err)
	}
	defer logFile.Close()

	// Build step context with log callback that emits events and writes to file.
	// lastChunkNewline tracks whether the most recent chunk ended with \n,
	// so Log knows whether it needs a leading \n to flush a streaming partial.
	lastChunkNewline := true
	userIntent := ""
	userIntentSource := ""
	if run != nil {
		if run.Intent != nil {
			userIntent = *run.Intent
		}
		// Propagate provenance alongside the text so steps can distinguish an
		// explicit, authoritative `--intent` (Source=="agent") from a
		// transcript-inferred hint. Dropping this is the provenance-erasure
		// bug that let an authoritative intent be demoted to an ignorable hint.
		if run.IntentSource != nil {
			userIntentSource = *run.IntentSource
		}
	}
	lastLogActivityAt := time.Time{}
	touchLogActivity := func(text string, force bool) {
		if activity := stepActivityFromLog(text); activity != "" {
			now := time.Now()
			if !force && !lastLogActivityAt.IsZero() && now.Sub(lastLogActivityAt) < stepActivityThrottleInterval {
				return
			}
			lastLogActivityAt = now
			if dbErr := e.db.TouchStepActivity(sr.ID, activity); dbErr != nil {
				slog.Warn("failed to touch step activity in db", "step", stepName, "error", dbErr)
			}
		}
	}
	writeLog := func(text string) {
		if text != "" {
			prefix := ""
			if !lastChunkNewline {
				prefix = "\n"
			}
			text = prefix + strings.TrimRight(text, "\n") + "\n\n"
			lastChunkNewline = true
		}
		e.emitLogChunk(run, repo, stepName, text)
		fmt.Fprint(logFile, text)
		touchLogActivity(text, true)
	}
	writeLogChunk := func(text string) {
		if text != "" {
			lastChunkNewline = strings.HasSuffix(text, "\n")
		}
		e.emitLogChunk(run, repo, stepName, text)
		fmt.Fprint(logFile, text)
		touchLogActivity(text, strings.Contains(text, "\n"))
	}
	onAgentLifecycle := func(event agent.LifecycleEvent) {
		text := event.Message
		if text == "" {
			text = fmt.Sprintf("%s %s", event.Agent, event.Phase)
		}
		switch event.Phase {
		case agent.LifecyclePhaseStart:
			pid := event.PID
			if dbErr := e.db.SetStepAgentActivity(sr.ID, text, &pid); dbErr != nil {
				slog.Warn("failed to set step agent activity in db", "step", stepName, "error", dbErr)
			}
		case agent.LifecyclePhaseExit:
			if dbErr := e.db.SetStepAgentActivity(sr.ID, text, nil); dbErr != nil {
				slog.Warn("failed to set step agent activity in db", "step", stepName, "error", dbErr)
			}
		default:
			if dbErr := e.db.TouchStepActivity(sr.ID, text); dbErr != nil {
				slog.Warn("failed to touch step activity in db", "step", stepName, "error", dbErr)
			}
		}
		writeLog(text)
	}
	// roundNum is shared with the perf wrapper's round closure below: an
	// invocation during execution of round N+1 sees roundNum still at N.
	autoFixAttempts := state.autoFixAttempts
	roundNum := state.roundNum
	carryFindings := findingsMayBeScopeLimited(step)
	carriedFindings := state.carriedFindings
	if !carryFindings {
		carriedFindings = ""
	}
	knownLineages := ""
	if sr.FindingsJSON != nil {
		knownLineages = *sr.FindingsJSON
	}

	stepAgent := e.agent
	if stepAgent != nil {
		stepAgent = &gateStepBoundaryAgent{inner: stepAgent, phase: stepName}
		stepAgent = &lifecycleAgent{inner: stepAgent, onLifecycle: onAgentLifecycle}
		stepAgent = &perfRecordingAgent{
			inner:    stepAgent,
			db:       e.db,
			runID:    run.ID,
			stepName: stepName,
			round:    func() int { return roundNum + 1 },
		}
	}
	ciReady := run.CIReadyAt != nil
	ciReadyNoCI := run.CIReadyNoCI
	ciReadinessChanged := func(ready, declaredNoCI bool) {
		declaredNoCI = ready && declaredNoCI
		if ciReady == ready && ciReadyNoCI == declaredNoCI {
			return
		}
		ciReady = ready
		ciReadyNoCI = declaredNoCI
		e.emitCIReadinessEvent(run, repo, ready, declaredNoCI)
	}
	sctx := &StepContext{
		Ctx:              ctx,
		Run:              run,
		Repo:             repo,
		WorkDir:          workDir,
		Agent:            stepAgent,
		Config:           e.config,
		DB:               e.db,
		StepResultID:     sr.ID,
		UserIntent:       userIntent,
		IntentSource:     userIntentSource,
		Sessions:         e.sessions,
		Shared:           e.shared,
		EvidenceDir:      e.runEvidenceDir(run.ID),
		Fixing:           state.fixing,
		PreviousFindings: state.previousFindings,
		Log:              writeLog,
		LogChunk:         writeLogChunk,
		LogFile: func(text string) {
			fmt.Fprintln(logFile, text)
			touchLogActivity(text, true)
		},
		CIReadinessChanged: ciReadinessChanged,
		OnPRMerged:         e.onPRMerged,
	}
	if stepName == types.StepReview {
		BindUncertifiedPipelineRange(sctx)
	}

	nextTrigger := "initial"
	if sctx.Fixing {
		nextTrigger = "auto_fix"
	}
	skipRemaining := false
	stepSkipped := false
	currentRoundID := state.currentRoundID
	var reviewApprovedHeadSHA string
	var restartFrom types.StepName

	// Execute with possible fix loop
	for {
		reviewStartingHeadSHA := run.HeadSHA
		sctx.ReviewStartingHeadSHA = reviewStartingHeadSHA
		outcome, err := step.Execute(sctx)
		roundNum++
		roundDuration := time.Since(phaseStart).Milliseconds()
		if err != nil {
			durationMS := executionMS + roundDuration
			// Persist the failure reason to the step's own log file. The error
			// often carries the only detail of why the step failed (e.g. git
			// stderr from a rejected push); without this the step log shows the
			// work starting but never why it stopped. Redact defensively so a
			// credentialled upstream URL that slipped into a wrapped error can
			// never land in the log file.
			redactedErr := safeurl.RedactText(err.Error())
			fmt.Fprintf(logFile, "\nerror: %s\n", redactedErr)
			touchLogActivity("error: "+redactedErr, true)
			if dbErr := e.db.FailStep(sr.ID, redactedErr, durationMS); dbErr != nil {
				slog.Warn("failed to mark step as failed in db", "step", stepName, "error", dbErr)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusFailed), "", redactedErr, &durationMS)
			return false, "", fmt.Errorf("step %s failed: %s", stepName, redactedErr)
		}
		restartFrom = outcome.RestartFrom

		if stepName == types.StepReview {
			reviewApprovedHeadSHA = outcome.ReviewApprovedHeadSHA
		}
		outcome.Findings, err = normalizeFindingsJSON(outcome.Findings, string(stepName), knownLineages)
		if err != nil {
			return false, "", fmt.Errorf("normalize %s findings: %w", stepName, err)
		}
		finalExitCode = outcome.ExitCode
		durationOverrideMS += outcome.DurationOverrideMS
		effectiveFindings := outcome.Findings
		if carryFindings {
			effectiveFindings = mergeCarriedFindingsJSON(outcome.Findings, carriedFindings, string(stepName))
		}
		if effectiveFindings != "" {
			knownLineages = effectiveFindings
		}

		if !carryFindings {
			if effectiveFindings != "" {
				if dbErr := e.db.SetStepFindings(sr.ID, effectiveFindings); dbErr != nil {
					slog.Warn("failed to set step findings in db", "step", stepName, "error", dbErr)
				}
			} else {
				if dbErr := e.db.ClearStepFindings(sr.ID); dbErr != nil {
					slog.Warn("failed to clear step findings in db", "step", stepName, "error", dbErr)
				}
			}
		}

		// Persist this execution round.
		var findingsPtr *string
		if effectiveFindings != "" {
			findingsPtr = &effectiveFindings
		}
		var fixSummaryPtr *string
		if outcome.FixSummary != "" {
			s := outcome.FixSummary
			fixSummaryPtr = &s
		}
		var inserted *db.StepRound
		var dbErr error
		roundTrigger := nextTrigger
		if stepName == types.StepCI && restartFrom != "" && !sctx.Fixing {
			roundTrigger = "auto_fix"
		}
		if carryFindings {
			trustedConfigSHA := ""
			var globalConfigYAML, repoConfigYAML []byte
			if e.config != nil && e.config.CaptureEvalProvenance {
				trustedConfigSHA = e.config.TrustedConfigSHA
				globalConfigYAML = e.config.ReplayGlobalYAML
				repoConfigYAML = e.config.ReplayRepoYAML
			}
			inserted, dbErr = e.db.InsertEffectiveReviewStepRoundWithProvenance(sr.ID, roundNum, roundTrigger, findingsPtr, fixSummaryPtr, reviewApprovedHeadSHA, reviewStartingHeadSHA, trustedConfigSHA, globalConfigYAML, repoConfigYAML, roundDuration)
		} else if stepName == types.StepReview {
			if e.config != nil && e.config.CaptureEvalProvenance {
				inserted, dbErr = e.db.InsertReviewStepRoundWithProvenance(sr.ID, roundNum, roundTrigger, findingsPtr, fixSummaryPtr, reviewApprovedHeadSHA, reviewStartingHeadSHA, e.config.TrustedConfigSHA, e.config.ReplayGlobalYAML, e.config.ReplayRepoYAML, roundDuration)
			} else {
				inserted, dbErr = e.db.InsertReviewStepRound(sr.ID, roundNum, roundTrigger, findingsPtr, fixSummaryPtr, reviewApprovedHeadSHA, roundDuration)
			}
		} else {
			inserted, dbErr = e.db.InsertStepRound(sr.ID, roundNum, roundTrigger, findingsPtr, fixSummaryPtr, roundDuration)
		}
		if dbErr != nil {
			currentRoundID = roundInsertID(currentRoundID, inserted, dbErr)
			if carryFindings {
				return false, "", fmt.Errorf("persist %s round %d: %w", stepName, roundNum, dbErr)
			}
			slog.Warn("failed to insert step round", "step", stepName, "round", roundNum, "error", dbErr)
		} else {
			currentRoundID = roundInsertID(currentRoundID, inserted, nil)
		}

		// If the step produced a PR URL, propagate it to the run and emit an update.
		if outcome.PRURL != "" {
			run.PRURL = &outcome.PRURL
			e.emitRunEvent(ipc.EventRunUpdated, run, repo)
		}

		// Review-loop convergence: rebuild and persist the report after every
		// review round so status surfaces always carry the per-round history.
		// A tripped guard is advisory - it stops the pipeline from spending
		// further automatic fix rounds and parks the gate for an explicit
		// decision, but never aborts the run and never blocks an explicit
		// approve, skip, or fix response.
		convergenceTripped := false
		if stepName == types.StepReview {
			report := e.evaluateReviewConvergence(ctx, sr.ID, run, workDir)
			if report.Tripped() {
				convergenceTripped = true
				writeLog("convergence warning: " + report.Warning)
			}
		}

		// Check if auto-fix should be attempted.
		// Only auto-fix findings whose action is "auto-fix".
		// This runs before the NeedsApproval check so that all severity
		// levels (including "info") get a chance at automatic fixing.
		if outcome.AutoFixable && autoFixLimit > 0 && autoFixAttempts < autoFixLimit && !convergenceTripped {
			roundOwnFindings := effectiveFindings
			if carryFindings {
				roundOwnFindings = retainMatchingFindingsJSON(effectiveFindings, outcome.Findings)
			}
			fixableFindings := autoFixableFindingsJSON(roundOwnFindings)
			if fixableFindings != "" {
				autoFixAttempts++
				telemetry.Track("fix", e.fixTelemetryFields("auto", stepName, findingsCount(fixableFindings), autoFixAttempts))
				slog.Info("auto-fixing step", "step", stepName, "attempt", autoFixAttempts, "max", autoFixLimit)
				executionMS += time.Since(phaseStart).Milliseconds()
				fixCount := findingsCount(fixableFindings)
				writeLog(fmt.Sprintf("auto-fix round %d/%d starting after round %d (%d %s)", autoFixAttempts, autoFixLimit, roundNum, fixCount, pluralize(fixCount, "finding", "findings")))
				if err := e.persistAutoFixSelection(currentRoundID, fixableFindings); err != nil {
					if carryFindings {
						return false, "", fmt.Errorf("record %s auto-fix selection: %w", stepName, err)
					}
					slog.Warn("failed to record selected finding ids", "step", stepName, "round", roundNum, "error", err)
				}
				if dbErr := e.db.UpdateStepStatus(sr.ID, types.StepStatusFixing); dbErr != nil {
					slog.Warn("failed to update step status in db", "step", stepName, "status", "fixing", "error", dbErr)
				}
				e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusFixing), "", "", nil)
				phaseStart = time.Now()
				sctx.Fixing = true
				sctx.PreviousFindings = fixableFindings
				nextTrigger = "auto_fix"
				if carryFindings {
					carriedFindings = excludeFindingsJSON(effectiveFindings, findingIDList(fixableFindings))
				}
				continue
			}
		}

		carryRequiresApproval := carryFindings && actionableFindingsCountJSON(carriedFindings) > 0
		if !outcome.NeedsApproval && !hasAskUserFindingsJSON(effectiveFindings) && !carryRequiresApproval &&
			!(convergenceTripped && actionableFindingsCountJSON(effectiveFindings) > 0) {
			// Step completed without needing approval.
			// Any remaining info-only or non-blocking findings
			// are acceptable and don't block the pipeline.
			// A tripped convergence guard with actionable findings falls
			// through to the park below instead: completing here would let a
			// non-converging loop end as "passed" carrying the very findings
			// the guard flagged, adjudicated by nobody.
			skipRemaining = outcome.SkipRemaining
			stepSkipped = outcome.Skipped
			break
		}

		// Freeze execution timer before entering approval wait.
		executionMS += time.Since(phaseStart).Milliseconds()

		// Determine approval status: fix_review after a fix cycle, awaiting_approval otherwise.
		// The working-tree diff that shows what the agent changed is NOT
		// attached here: it is unbounded, and one frame over the transport
		// limit kills the whole subscription and hides every event after it.
		// Consumers fetch it on demand from the run's worktree instead
		// (ipc.MethodGetStepDiff).
		approvalStatus := types.StepStatusAwaitingApproval
		if sctx.Fixing {
			approvalStatus = types.StepStatusFixReview
		}

		// Mark executor as ready to receive approval before updating DB or
		// emitting events, so that callers who poll the DB status can
		// immediately call Respond once they see it.
		e.mu.Lock()
		e.waiting = true
		e.waitingStep = stepName
		e.mu.Unlock()

		// Parking starts before the gate becomes observable. This includes the
		// small handoff from publishing the gate to receiving a response, and
		// prevents a prompt response from being omitted from the parked total.
		parkStart := time.Now()

		// Surface the park as a pollable, run-level signal so a supervisor can
		// tell in one `axi status` read that the run is waiting for the agent
		// to drive this gate (versus actively running/fixing/ci). Observability
		// only: it does not change the wait below. Cleared once the wait ends.
		if dbErr := e.db.ParkStepForApproval(run.ID, sr.ID, approvalStatus, executionMS, findingsPtr); dbErr != nil {
			e.mu.Lock()
			e.waiting = false
			e.waitingStep = ""
			e.mu.Unlock()
			return false, "", fmt.Errorf("persist %s approval gate: %w", stepName, dbErr)
		}
		e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(approvalStatus), effectiveFindings, "", &executionMS)

		response, reconciled, err := e.waitForApprovalOrReconcile(ctx, step, sctx, true)
		if dbErr := e.db.CompleteRunAwaitingAgent(run.ID, time.Since(parkStart).Milliseconds()); dbErr != nil {
			slog.Warn("failed to complete awaiting-agent state in db", "step", stepName, "run", run.ID, "error", dbErr)
		}
		if err != nil {
			if dbErr := e.db.FailStep(sr.ID, err.Error(), executionMS); dbErr != nil {
				slog.Warn("failed to mark step as failed in db", "step", stepName, "error", dbErr)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusFailed), "", err.Error(), &executionMS)
			return false, "", fmt.Errorf("step %s: waiting for approval: %w", stepName, err)
		}
		if reconciled {
			phaseStart = time.Now()
			goto done
		}

		approvalFields := telemetry.Fields{
			"step":       string(stepName),
			"action":     string(response.action),
			"fix_review": sctx.Fixing,
		}
		if agentName := e.telemetryAgentName(); agentName != "" {
			approvalFields["agent"] = agentName
		}
		if selectedCount := selectedFindingCount(effectiveFindings, response.findingIDs); selectedCount > 0 {
			approvalFields["selected_findings_count"] = selectedCount
		}
		telemetry.Track("approval", approvalFields)

		switch response.action {
		case types.ActionApprove:
			// Approved - execution already frozen in executionMS, reset phaseStart
			// so the done label computes no additional elapsed.
			// An approve that leaves actionable findings behind is a deliberate
			// adjudication by the approver; record it durably in the step log so
			// a "passed" run carrying unapplied findings is always attributable
			// to an explicit decision, never a silent default.
			if n := actionableFindingsCountJSON(effectiveFindings); n > 0 {
				writeLog(fmt.Sprintf("gate approved with %d unresolved actionable %s; approval recorded as explicit adjudication", n, pluralize(n, "finding", "findings")))
			}
			phaseStart = time.Now()
			goto done

		case types.ActionSkip:
			// Skip - mark step skipped and return (not an error)
			if err := e.db.CompleteStepWithStatus(sr.ID, types.StepStatusSkipped, finalExitCode, executionMS, logPath); err != nil {
				return false, "", fmt.Errorf("complete step %s (skip): %w", stepName, err)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusSkipped), "", "", &executionMS)
			return false, "", nil

		case types.ActionAbort:
			if dbErr := e.db.FailStep(sr.ID, "aborted by user", executionMS); dbErr != nil {
				slog.Warn("failed to mark step as failed in db", "step", stepName, "error", dbErr)
			}
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusFailed), "", "aborted by user", &executionMS)
			return false, "", fmt.Errorf("step %s: aborted by user", stepName)

		case types.ActionFix:
			telemetry.Track("fix", e.fixTelemetryFields("user", stepName, selectedFindingCount(effectiveFindings, response.findingIDs), 0))
			// Fix - mark step as fixing, resume execution timer, re-execute.
			phaseStart = time.Now()
			selectedCount := selectedFindingCount(effectiveFindings, response.findingIDs)
			writeLog(fmt.Sprintf("user-fix round starting after round %d (%d %s selected)", roundNum, selectedCount, pluralize(selectedCount, "finding", "findings")))
			selectedFindings := filterFindingsJSON(effectiveFindings, response.findingIDs)
			mergedFindings := mergeUserOverridesJSON(selectedFindings, response.instructions, response.addedFindings)
			if err := e.persistUserFixDecision(currentRoundID, response.findingIDs, selectedFindings, mergedFindings); err != nil {
				if carryFindings {
					return false, "", fmt.Errorf("record %s user decision: %w", stepName, err)
				}
				slog.Warn("failed to record user decision", "step", stepName, "round", roundNum, "error", err)
			}
			if dbErr := e.db.UpdateStepStatus(sr.ID, types.StepStatusFixing); dbErr != nil {
				slog.Warn("failed to update step status in db", "step", stepName, "status", "fixing", "error", dbErr)
			}
			sctx.Fixing = true
			sctx.PreviousFindings = mergedFindings
			if carryFindings {
				carriedFindings = excludeFindingsJSON(effectiveFindings, response.findingIDs)
			}
			nextTrigger = "auto_fix"
			e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(types.StepStatusFixing), "", "", nil)
			slog.Info("step fix requested, re-executing", "step", stepName)
			continue // loop back to step.Execute
		}
	}

done:
	// Mark step completed with execution-only timing.
	durationMS := executionMS + time.Since(phaseStart).Milliseconds()
	if durationOverrideMS > 0 {
		durationMS = durationOverrideMS
	}
	status := types.StepStatusCompleted
	if stepSkipped {
		status = types.StepStatusSkipped
	}
	// A review round's captured head becomes authority only when the review
	// actually completes. Parked outcomes stay in the loop above, failures
	// return earlier, and skipped reviews deliberately leave the binding empty.
	// Completion and authority replacement are one DB transaction.
	if stepName == types.StepReview && status == types.StepStatusCompleted && reviewApprovedHeadSHA != "" {
		if err := e.db.CompleteReviewStep(sr.ID, run.ID, reviewApprovedHeadSHA, finalExitCode, durationMS, logPath); err != nil {
			return false, "", fmt.Errorf("complete step %s: %w", stepName, err)
		}
		reviewedHead := reviewApprovedHeadSHA
		run.ReviewApprovedHeadSHA = &reviewedHead
		ClearUncertifiedPipelineRangeIfCertified(ctx, e.db, repo.ID, run.Branch, reviewedHead, workDir)
	} else if err := e.db.CompleteStepWithStatusAtHead(sr.ID, status, run.HeadSHA, finalExitCode, durationMS, logPath); err != nil {
		return false, "", fmt.Errorf("complete step %s: %w", stepName, err)
	}
	e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, stepName, string(status), "", "", &durationMS)
	return skipRemaining, restartFrom, nil
}

func (e *Executor) persistAutoFixSelection(roundID, findings string) error {
	idsJSON := findingIDsJSON(findings)
	if idsJSON == "" {
		return nil
	}
	if roundID == "" {
		return errors.New("step round is not durable")
	}
	return e.db.SetStepRoundSelection(roundID, &idsJSON, db.RoundSelectionSourceAutoFix)
}

func (e *Executor) persistUserFixDecision(roundID string, selectedIDs []string, selected, merged string) error {
	idsJSON := marshalFindingIDs(combineSelectedFindingIDs(selectedIDs, merged))
	if idsJSON == "" {
		return nil
	}
	if roundID == "" {
		return errors.New("step round is not durable")
	}
	var userFindingsJSON *string
	if merged != "" && merged != selected {
		userFindingsJSON = &merged
	}
	return e.db.SetStepRoundUserDecision(roundID, &idsJSON, db.RoundSelectionSourceUser, userFindingsJSON)
}

func roundInsertID(_ string, inserted *db.StepRound, err error) string {
	if err != nil || inserted == nil {
		return ""
	}
	return inserted.ID
}

type gateStepBoundaryAgent struct {
	inner agent.Agent
	phase types.StepName
}

func (a *gateStepBoundaryAgent) Name() string { return a.inner.Name() }

func (a *gateStepBoundaryAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	opts.Prompt = gateguidance.PromptBoundary(string(a.phase)) + opts.Prompt
	return a.inner.Run(ctx, opts)
}

func (a *gateStepBoundaryAgent) Close() error { return a.inner.Close() }

func (a *gateStepBoundaryAgent) SupportsSessionResume() bool {
	return agent.SupportsSessionResume(a.inner)
}

func (a *gateStepBoundaryAgent) SupportsSessionProvider(provider string) bool {
	return agent.SupportsSessionProvider(a.inner, provider)
}

func (a *gateStepBoundaryAgent) ReportsAgentAttempts() bool {
	return agent.ReportsAgentAttempts(a.inner)
}

func (a *gateStepBoundaryAgent) NeutralizesGateInstructions() bool {
	return agent.NeutralizesGateInstructions(a.inner)
}

type lifecycleAgent struct {
	inner       agent.Agent
	onLifecycle func(agent.LifecycleEvent)
}

func (a *lifecycleAgent) Name() string {
	return a.inner.Name()
}

func (a *lifecycleAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	previous := opts.OnLifecycle
	opts.OnLifecycle = func(event agent.LifecycleEvent) {
		if previous != nil {
			previous(event)
		}
		if a.onLifecycle != nil {
			a.onLifecycle(event)
		}
	}
	return a.inner.Run(ctx, opts)
}

func (a *lifecycleAgent) Close() error {
	return a.inner.Close()
}

// SupportsSessionResume forwards the wrapped adapter's session capability so
// wrapping never hides it from the review loop's session manager.
func (a *lifecycleAgent) SupportsSessionResume() bool {
	return agent.SupportsSessionResume(a.inner)
}

func (a *lifecycleAgent) SupportsSessionProvider(provider string) bool {
	return agent.SupportsSessionProvider(a.inner, provider)
}

func (a *lifecycleAgent) ReportsAgentAttempts() bool {
	return agent.ReportsAgentAttempts(a.inner)
}

const (
	maxStepActivityText          = 240
	stepActivityThrottleInterval = time.Second
)

func stepActivityFromLog(text string) string {
	end := len(text)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	if end == 0 {
		return ""
	}
	start := strings.LastIndexByte(text[:end], '\n') + 1
	line := strings.TrimSpace(text[start:end])
	return "log: " + truncateActivity(line)
}

func truncateActivity(text string) string {
	if len(text) <= maxStepActivityText {
		return text
	}
	runeCount := 0
	for i := range text {
		if runeCount == maxStepActivityText {
			return text[:i] + "..."
		}
		runeCount++
	}
	return text
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// waitForApprovalOrReconcile blocks until a user action arrives, the parked
// gate's external source of truth makes it obsolete, or the context is
// cancelled. Reconciliation runs synchronously under a bounded child context,
// so no watcher goroutine can outlive approval, cancellation, or shutdown.
// The caller must set e.waiting and e.waitingStep before calling this method.
func (e *Executor) waitForApprovalOrReconcile(ctx context.Context, step Step, sctx *StepContext, immediate bool) (approvalResponse, bool, error) {
	defer func() {
		e.mu.Lock()
		e.waiting = false
		e.waitingStep = ""
		e.mu.Unlock()
		// Drain any stale response that arrived after context cancellation or
		// raced with an external reconciliation.
		select {
		case <-e.approvalCh:
		default:
		}
	}()

	if _, ok := step.(ApprovalGateReconciler); !ok {
		select {
		case response := <-e.approvalCh:
			return response, false, nil
		case <-ctx.Done():
			return approvalResponse{}, false, context.Cause(ctx)
		}
	}

	delay := e.gateReconcileInterval
	if immediate {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case response := <-e.approvalCh:
			return response, false, nil
		case <-ctx.Done():
			return approvalResponse{}, false, context.Cause(ctx)
		case <-timer.C:
			resolved, err := e.reconcileApprovalGate(ctx, step, sctx)
			if resolved {
				if e.claimGateReconciliation() {
					return approvalResponse{}, true, nil
				}
				return <-e.approvalCh, false, nil
			}
			if errors.Is(err, ErrFatalGateReconciliation) {
				return approvalResponse{}, false, err
			}
			if err != nil && ctx.Err() == nil {
				if sctx != nil && sctx.Log != nil {
					sctx.Log(fmt.Sprintf("warning: could not reconcile parked %s gate; preserving it: %v", step.Name(), err))
				} else {
					slog.Warn("could not reconcile parked approval gate; preserving it", "step", step.Name(), "error", err)
				}
			}
			timer.Reset(e.gateReconcileInterval)
		}
	}
}

func (e *Executor) claimGateReconciliation() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.waiting {
		return false
	}
	e.waiting = false
	e.waitingStep = ""
	return true
}

func (e *Executor) reconcileApprovalGate(ctx context.Context, step Step, sctx *StepContext) (bool, error) {
	reconciler, ok := step.(ApprovalGateReconciler)
	if !ok {
		return false, nil
	}
	timeout := e.gateReconcileTimeout
	if timeout <= 0 {
		timeout = defaultGateReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	copyCtx := *sctx
	copyCtx.Ctx = reconcileCtx
	return reconciler.ReconcileApprovalGate(&copyCtx)
}

// failRun marks a run as failed and returns the error.
// It accepts an optional context; if the context was cancelled with a cause,
// the cause message is used as the run's error (more informative than "context canceled").
func (e *Executor) failRun(run *db.Run, repo *db.Repo, err error, ctxs ...context.Context) error {
	errMsg := err.Error()
	for _, ctx := range ctxs {
		if cause := context.Cause(ctx); cause != nil && cause != context.Canceled {
			errMsg = cause.Error()
			break
		}
	}
	runStatus := types.RunFailed
	if errMsg == types.RunCancelReasonAbortedByUser || errMsg == types.RunCancelReasonSuperseded {
		runStatus = types.RunCancelled
	}
	verifiedHead, verified := e.reconcileTerminalRunHead(run)
	var dbErr error
	if verified {
		dbErr = e.db.UpdateRunErrorStatusWithVerifiedHead(run.ID, errMsg, runStatus, verifiedHead)
	} else {
		dbErr = e.db.UpdateRunErrorStatus(run.ID, errMsg, runStatus)
	}
	if dbErr != nil {
		slog.Error("failed to update run error status", "run", run.ID, "error", dbErr)
	} else if verified {
		run.HeadSHA = verifiedHead
	}
	run.Status = runStatus
	run.Error = &errMsg
	e.emitRunEvent(ipc.EventRunCompleted, run, repo)
	return err
}

func (e *Executor) completeRun(run *db.Run, repo *db.Repo) error {
	verifiedHead, verified := e.reconcileTerminalRunHead(run)
	var err error
	if verified {
		err = e.db.UpdateRunStatusWithVerifiedHead(run.ID, types.RunCompleted, verifiedHead)
	} else {
		err = e.db.UpdateRunStatus(run.ID, types.RunCompleted)
	}
	if err != nil {
		return err
	}
	if verified {
		run.HeadSHA = verifiedHead
	}
	run.Status = types.RunCompleted
	e.emitRunEvent(ipc.EventRunCompleted, run, repo)
	return nil
}

func (e *Executor) reconcileTerminalRunHead(run *db.Run) (string, bool) {
	if run == nil || strings.TrimSpace(e.workDir) == "" {
		return "", false
	}
	recordedRun, err := e.db.GetRun(run.ID)
	if err != nil || recordedRun == nil {
		slog.Warn("failed to load run head before terminalization", "run", run.ID, "error", err)
		return "", false
	}
	recorded := strings.TrimSpace(recordedRun.HeadSHA)
	if recorded == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, err := git.HeadSHA(ctx, e.workDir)
	if err != nil {
		slog.Warn("failed to resolve worktree head before terminalization", "run", run.ID, "error", err)
		return "", false
	}
	observed = strings.TrimSpace(observed)
	if observed == "" {
		return "", false
	}
	if observed == recorded {
		return recorded, true
	}
	if _, err := git.Run(ctx, e.workDir, "merge-base", "--is-ancestor", recorded, observed); err != nil {
		slog.Warn("worktree head is not a verified descendant before terminalization", "run", run.ID, "error", err)
		return "", false
	}
	// The worktree is detached, so a head recorded on the run but held by no ref
	// dies with the worktree and strands the branch in pipeline custody with no
	// working recovery. Terminalization is the last writer of runs.head_sha, so
	// it owes the same adoption every step writer performs; when the guarded
	// move refuses (another push owns the branch), the run keeps the last
	// adopted head instead of recording one nothing references.
	if err := git.AdoptBranchRef(func(args ...string) (string, error) {
		return git.Run(ctx, e.workDir, args...)
	}, run.Branch, observed, recorded); err != nil {
		slog.Warn("worktree head could not be adopted on the branch ref before terminalization", "run", run.ID, "error", err)
		return "", false
	}
	return observed, true
}

// --- event helpers ---

func (e *Executor) emitRunEvent(eventType ipc.EventType, run *db.Run, repo *db.Repo) {
	status := string(run.Status)
	event := ipc.Event{
		Type:   eventType,
		RunID:  run.ID,
		RepoID: repo.ID,
		Status: &status,
		Branch: &run.Branch,
		Error:  run.Error,
		PRURL:  run.PRURL,
	}
	e.onEvent(event)
}

func (e *Executor) emitCIReadinessEvent(run *db.Run, repo *db.Repo, ready, declaredNoCI bool) {
	declaredNoCI = ready && declaredNoCI
	e.onEvent(ipc.Event{
		Type:        ipc.EventCIReadinessChanged,
		RunID:       run.ID,
		RepoID:      repo.ID,
		CIReady:     &ready,
		CIReadyNoCI: &declaredNoCI,
	})
}

func (e *Executor) emitStepEvent(eventType ipc.EventType, run *db.Run, repo *db.Repo, stepName types.StepName, status string) {
	e.emitStepEventWithFindings(eventType, run, repo, stepName, status, "")
}

func (e *Executor) emitStepEventWithFindings(eventType ipc.EventType, run *db.Run, repo *db.Repo, stepName types.StepName, status string, findings string) {
	e.emitStepEventWithFindingsAndError(eventType, run, repo, stepName, status, findings, "", nil)
}

func (e *Executor) emitStepEventWithFindingsAndError(eventType ipc.EventType, run *db.Run, repo *db.Repo, stepName types.StepName, status string, findings string, errMsg string, durationMS *int64) {
	event := ipc.Event{
		Type:       eventType,
		RunID:      run.ID,
		RepoID:     repo.ID,
		StepName:   &stepName,
		Status:     &status,
		DurationMS: durationMS,
	}
	stats := e.findingStatsForStep(run.ID, stepName)
	if stats.ReportedFindings > 0 || stats.FixedFindings > 0 {
		reported := stats.ReportedFindings
		fixed := stats.FixedFindings
		event.ReportedFindings = &reported
		event.FixedFindings = &fixed
	}
	if errMsg != "" {
		event.Error = &errMsg
	}
	if findings != "" {
		event.Findings = &findings
	}
	e.onEvent(event)
	if !shouldTrackStepTelemetry(eventType, status) {
		return
	}

	fields := telemetry.Fields{
		"event":  string(eventType),
		"step":   string(stepName),
		"status": status,
	}
	if agentName := e.telemetryAgentName(); agentName != "" {
		fields["agent"] = agentName
	}
	if durationMS != nil {
		fields["duration_ms"] = *durationMS
	}
	if findings != "" {
		fields["findings_count"] = findingsCount(findings)
	}
	telemetry.Track("step", fields)
}

func (e *Executor) findingStatsForStep(runID string, stepName types.StepName) db.StepStats {
	steps, err := e.db.GetStepsByRun(runID)
	if err != nil {
		return db.StepStats{StepName: stepName}
	}
	for _, step := range steps {
		if step.StepName != stepName {
			continue
		}
		stats, err := e.db.StepFindingStats(step)
		if err != nil {
			return db.StepStats{StepName: stepName}
		}
		return stats
	}
	return db.StepStats{StepName: stepName}
}

func shouldTrackStepTelemetry(eventType ipc.EventType, status string) bool {
	if eventType != ipc.EventStepCompleted {
		return false
	}
	switch types.StepStatus(status) {
	case types.StepStatusAwaitingApproval, types.StepStatusFixReview, types.StepStatusFailed:
		return true
	default:
		return false
	}
}

func (e *Executor) emitLogChunk(run *db.Run, repo *db.Repo, stepName types.StepName, content string) {
	e.onEvent(ipc.Event{
		Type:     ipc.EventLogChunk,
		RunID:    run.ID,
		RepoID:   repo.ID,
		StepName: &stepName,
		Content:  &content,
	})
}

func (e *Executor) telemetryAgentName() string {
	if e.config == nil || e.config.Agent == "" {
		return ""
	}
	return string(e.config.Agent)
}

func (e *Executor) fixTelemetryFields(source string, stepName types.StepName, selectedCount int, attempt int) telemetry.Fields {
	fields := telemetry.Fields{
		"source":                  source,
		"step":                    string(stepName),
		"selected_findings_count": selectedCount,
	}
	if agentName := e.telemetryAgentName(); agentName != "" {
		fields["agent"] = agentName
	}
	if attempt > 0 {
		fields["attempt"] = attempt
	}
	return fields
}

func findingsCount(raw string) int {
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return 0
	}
	return len(findings.Items)
}

func selectedFindingCount(raw string, ids []string) int {
	if len(ids) > 0 {
		return len(ids)
	}
	return findingsCount(raw)
}
