package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// LaneOutageError reports that one agent lane cannot run because the
// provider's quota is exhausted until Until. It wraps the provider failure
// that produced the mark when there is one, so the original banner still
// reaches the step log.
type LaneOutageError struct {
	Lane          string
	ScopeKey      string
	AccountID     string
	Model         string
	Until         time.Time
	Reason        string
	ResetTimezone string
	cause         error
}

func (e *LaneOutageError) Error() string {
	subject := "agent lane " + e.Lane
	if e.AccountID != "" && e.Model != "" {
		account := e.AccountID
		if len(account) > 12 {
			account = account[:12]
		}
		subject = fmt.Sprintf("agent lane %s account %s model %s", e.Lane, account, e.Model)
	}
	msg := fmt.Sprintf("%s is quota-exhausted until %s", subject, lanehealth.FormatResetTime(e.Until, e.ResetTimezone))
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *LaneOutageError) Unwrap() error { return e.cause }

// IsQuotaOutage reports whether err says provider quota exhaustion is what
// failed the invocation: a single lane's *LaneOutageError (skipped while
// marked, or freshly classified from the provider banner) or the fallback
// wrapper's every-eligible-lane aggregate. Telemetry classification keys on
// this instead of substring-matching error text, which embeds provider banner
// excerpts like "codex exited: ..." and would misfile the outage.
func IsQuotaOutage(err error) bool {
	var lane *LaneOutageError
	if errors.As(err, &lane) {
		return true
	}
	var all *allLanesOutageError
	return errors.As(err, &all)
}

// IsAgentUnavailable reports whether an invocation failed because the agent
// lane could not serve it, rather than because the work the agent was asked to
// assess produced a negative result. Agent adapters return successful Result
// values for structured findings; lease refusals, provider outages, timeouts,
// and process exits arrive as errors.
//
// This is the single availability classifier used both by fallback routing and
// by callers that must preserve an independently measured result when a later
// narration invocation dies. Keep quota recognition structural through
// IsQuotaOutage: matching its rendered text here would recreate the competing
// outage classifier this package exists to avoid.
func IsAgentUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var seatBlocked *SeatBlockedError
	if errors.As(err, &seatBlocked) {
		return true
	}
	if IsQuotaOutage(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if _, transient := classifyTransient(err); transient {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		" start:",
		"start server ",
		" server: start server ",
		" exited:",
		" reported exit code ",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// LaneHealthStore is the slice of lanehealth.Store this package needs, kept as
// an interface so tests and future callers can substitute their own.
type LaneHealthStore interface {
	Outage(scopeKey string) (lanehealth.Outage, bool)
	ClaimProbe(scopeKey string) bool
	Mark(outage lanehealth.Outage) error
	ClearObservedBefore(scopeKey string, startedAt time.Time) error
}

// LaneName returns the key a configured agent name's lane health is recorded
// under: the identity the constructed agent reports from Agent.Name(), which
// for every ACP-driven agent is its target rather than the alias the operator
// configured. Read surfaces must resolve through this so they cannot look up a
// lane the pipeline never writes.
func LaneName(name types.AgentName) string {
	if target, ok := types.ACPTargetFor(name); ok {
		return acpAgentName(target)
	}
	return string(name)
}

// laneHealthAgent skips an invocation while its resolved provider account and
// model are known to be quota-exhausted, and records the outage when a provider
// quota banner is what failed the invocation.
//
// Marking happens here rather than in the fallback wrapper so a single
// configured account/model scope also fails fast with a reset time instead of
// spawning a process to be told it is out of quota.
//
// A marked account/model scope is not sealed until its reset time: one probe per
// lanehealth.ProbeInterval is let through, and its success clears the mark. A
// reset the provider stated days out is otherwise trusted from one observation
// with no way to correct it, because the only evidence that could - a
// completed invocation - is what the mark suppresses.
type laneHealthAgent struct {
	Agent
	store LaneHealthStore
	now   func() time.Time
}

func (l laneHealthAgent) InvocationIdentity() InvocationIdentity {
	return ResolveInvocationIdentity(l.Agent)
}

// WithLaneHealth wraps a single agent lane with persisted quota-outage
// tracking. A nil store returns the agent unchanged, so demo mode and tests
// that do not care keep the previous behavior exactly.
func WithLaneHealth(a Agent, store LaneHealthStore, now func() time.Time) Agent {
	if a == nil {
		return nil
	}
	if store == nil {
		return a
	}
	if now == nil {
		now = time.Now
	}
	return laneHealthAgent{Agent: a, store: store, now: now}
}

func (l laneHealthAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	opts = withInvocationIdentity(opts, l.Agent)
	_, scoped := l.Agent.(QuotaScopeReporter)
	probeRunner, probeable := l.Agent.(QuotaProbeRunner)
	if !scoped || !probeable {
		// An exact home/model identity and a one-shot probe are both required.
		// Unknown wrappers remain usable, but they cannot create a broad lane
		// mark that suppresses unrelated accounts or models.
		return l.Agent.Run(ctx, opts)
	}
	scope, resolved := ResolveQuotaScope(l.Agent, opts)
	if !resolved || scope.Key == "" {
		return l.Agent.Run(ctx, opts)
	}
	lane := l.Agent.Name()
	startedAt := l.now()
	resetLocation := quotaObservationTime(startedAt, opts.Env).Location()
	// A lane that reports its own attempts emits them from below this wrapper,
	// carrying the raw provider error, before the outage verdict exists. This
	// wrapper therefore owns attempt fidelity for such a lane. A lane that does
	// not report attempts is reported by the caller from this wrapper's return
	// value, which already carries the verdict.
	reportsAttempts := ReportsAgentAttempts(l.Agent)
	var relay *attemptRelay
	if opts.OnAttempt != nil && reportsAttempts {
		relay = &attemptRelay{downstream: opts.OnAttempt}
		opts.OnAttempt = relay.capture
	}
	defer relay.release()

	if outage, down := l.store.Outage(scope.Key); down {
		if !l.store.ClaimProbe(scope.Key) {
			err := laneOutageError(lane, outage, nil)
			if opts.OnChunk != nil {
				opts.OnChunk("\n" + err.Error() + "\n")
			}
			if reportsAttempts {
				// A skipped lane never reaches the adapter that would report it, so
				// without this the skip leaves no record at all whenever a fallback
				// set moves on to another lane.
				emitAgentAttempt(opts, lane, nil, err, startedAt, l.now())
			}
			return nil, err
		}
		if opts.OnChunk != nil {
			opts.OnChunk(fmt.Sprintf("\nagent lane %s account %s model %s is marked quota-exhausted until %s; sending one bounded probe to check for early recovery\n",
				lane, shortAccountID(scope.AccountID), scope.Model, outage.ResetTime()))
		}
		probeStarted := l.now()
		probeOpts, cleanupProbeDir, probeOptsErr := isolatedQuotaProbeOpts(opts, scope)
		if probeOptsErr != nil {
			err := laneOutageError(lane, outage, probeOptsErr)
			relay.amend(err)
			return nil, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, lanehealth.ProbeTimeout)
		_, probeErr := func() (*Result, error) {
			defer cleanupProbeDir()
			return probeRunner.RunQuotaProbe(probeCtx, probeOpts)
		}()
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if probeErr != nil {
			if refreshed, quota := lanehealth.Classify(lane, probeErr.Error(), l.now().In(resetLocation)); quota {
				refreshed.ScopeKey = scope.Key
				refreshed.AccountID = scope.AccountID
				refreshed.Model = scope.Model
				_ = l.store.Mark(refreshed)
				err := laneOutageError(lane, refreshed, probeErr)
				relay.amend(err)
				return nil, err
			}
			// An unavailable probe is not evidence that the quota recovered. Keep
			// the existing mark and its backoff, then route around this account.
			err := laneOutageError(lane, outage, probeErr)
			relay.amend(err)
			return nil, err
		}
		_ = l.store.ClearObservedBefore(scope.Key, probeStarted)
		if current, stillMarked := l.store.Outage(scope.Key); stillMarked {
			// A concurrent run can have observed a new outage while the probe was
			// running. Its newer evidence survives the success and still blocks.
			err := laneOutageError(lane, current, nil)
			relay.amend(err)
			return nil, err
		}
		if opts.OnChunk != nil {
			opts.OnChunk(fmt.Sprintf("\nagent lane %s account %s model %s recovered; continuing the requested invocation\n",
				lane, shortAccountID(scope.AccountID), scope.Model))
		}
	}

	startedAt = l.now()
	resetLocation = quotaObservationTime(startedAt, opts.Env).Location()
	result, err := l.Agent.Run(ctx, opts)
	if err == nil {
		// A completed invocation is direct evidence the lane worked when it was
		// authorized, so any mark that predates it - including one written from a
		// misread banner - is dropped rather than left to expire on its own. A mark
		// a concurrent run observed after this invocation started describes a later
		// state of the account and survives.
		_ = l.store.ClearObservedBefore(scope.Key, startedAt)
		return result, nil
	}
	if ctx.Err() != nil {
		// A cancelled or timed-out run says nothing about the lane's quota, and
		// its partial output may still carry a banner the provider had only
		// warned about. Never park a lane on that evidence.
		return result, err
	}
	var parseErr *OutputParseError
	if errors.As(err, &parseErr) {
		// A schema-parse failure is the one adapter error built from the agent's
		// own final message, and that message can quote a banner verbatim - a
		// review of this very repository does. Classifying it would park a
		// healthy lane, so this failure never reaches the classifier.
		return result, err
	}
	// Only a failed invocation is classified, and its text comes from the
	// provider's stderr and error channel, never from agent-authored output.
	if outage, quota := lanehealth.Classify(lane, err.Error(), l.now().In(resetLocation)); quota {
		outage.ScopeKey = scope.Key
		outage.AccountID = scope.AccountID
		outage.Model = scope.Model
		_ = l.store.Mark(outage)
		outageErr := laneOutageError(lane, outage, err)
		relay.amend(outageErr)
		return nil, outageErr
	}
	return result, err
}

func laneOutageError(lane string, outage lanehealth.Outage, cause error) *LaneOutageError {
	return &LaneOutageError{
		Lane:          lane,
		ScopeKey:      outage.ScopeKey,
		AccountID:     outage.AccountID,
		Model:         outage.Model,
		Until:         outage.Until,
		Reason:        outage.Reason,
		ResetTimezone: outage.ResetTimezone,
		cause:         cause,
	}
}

func shortAccountID(accountID string) string {
	if len(accountID) > 12 {
		return accountID[:12]
	}
	return accountID
}

// attemptRelay defers a lane's most recent adapter attempt so the lane wrapper
// can re-report its error as the quota outage it classified only after the
// adapter has already returned. Earlier retry attempts pass through untouched,
// and their order is preserved because attempt N is released the moment
// attempt N+1 arrives. Every method tolerates a nil relay so a lane with no
// attempt reporting needs no branch at the call sites.
type attemptRelay struct {
	downstream func(Attempt)
	held       *Attempt
}

func (r *attemptRelay) capture(attempt Attempt) {
	if r == nil {
		return
	}
	r.release()
	held := attempt
	r.held = &held
}

// amend replaces the held attempt's error with the verdict the wrapper reached,
// so the recorded attempt and the caller's error agree on why the lane failed.
func (r *attemptRelay) amend(err error) {
	if r == nil || r.held == nil {
		return
	}
	r.held.Err = err
}

func (r *attemptRelay) release() {
	if r == nil || r.held == nil {
		return
	}
	attempt := *r.held
	r.held = nil
	r.downstream(attempt)
}

func (l laneHealthAgent) SupportsSessionResume() bool {
	return SupportsSessionResume(l.Agent)
}

func (l laneHealthAgent) SupportsSessionProvider(provider string) bool {
	return SupportsSessionProvider(l.Agent, provider)
}

func (l laneHealthAgent) ReportsAgentAttempts() bool {
	return ReportsAgentAttempts(l.Agent)
}

func (l laneHealthAgent) NeutralizesGateInstructions() bool {
	return NeutralizesGateInstructions(l.Agent)
}

// quotaObservationTime uses the same last-wins TZ environment as the child.
// A timezone in the provider banner still takes precedence in Classify. An
// absent or unsupported TZ retains the daemon location; never guess Central
// just because an operator happens to read the banner there.
func quotaObservationTime(now time.Time, env []string) time.Time {
	zone, set := os.LookupEnv("TZ")
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "TZ="); ok {
			zone, set = value, true
		}
	}
	if !set {
		return now
	}
	if zone == "" {
		return now.In(time.UTC)
	}
	if loc, err := time.LoadLocation(strings.TrimPrefix(zone, ":")); err == nil {
		return now.In(loc)
	}
	return now
}
