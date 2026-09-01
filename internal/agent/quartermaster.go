package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
)

const defaultQuartermasterTTL = 30 * time.Minute

type QuartermasterLease struct {
	Account string
	ID      string
	Until   time.Time
	Pool    string
	Holder  string
}

type QuartermasterAcquireRequest struct {
	Pool    string
	Holder  string
	Purpose string
	TTL     time.Duration
	Weight  int
}

type QuartermasterClient interface {
	Acquire(context.Context, QuartermasterAcquireRequest) (QuartermasterLease, error)
	Release(context.Context, QuartermasterLease, string) error
}

type QuartermasterOptions struct {
	Client QuartermasterClient
	Pool   string
	Holder string
	TTL    time.Duration
	Weight int
	Home   string
}

type QuartermasterRefusalError struct {
	Pool    string
	Purpose string
	Reason  string
}

func (e *QuartermasterRefusalError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "no lease granted"
	}
	if e.Purpose == "" {
		return fmt.Sprintf("quartermaster refused %s lease: %s", e.Pool, reason)
	}
	return fmt.Sprintf("quartermaster refused %s lease for %s: %s", e.Pool, e.Purpose, reason)
}

type commandQuartermasterClient struct {
	Bin string
}

func NewCommandQuartermasterClient(bin string) QuartermasterClient {
	return commandQuartermasterClient{Bin: bin}
}

func (c commandQuartermasterClient) Acquire(ctx context.Context, req QuartermasterAcquireRequest) (QuartermasterLease, error) {
	ttl := req.TTL
	if ttl <= 0 {
		ttl = defaultQuartermasterTTL
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	bin := c.Bin
	if strings.TrimSpace(bin) == "" {
		bin = defaultQuartermasterBin()
	}
	args := []string{
		"acquire",
		"--pool", req.Pool,
		"--holder", req.Holder,
		"--job", quartermasterIdent(req.Purpose, time.Now()),
		"--ttl", strconv.Itoa(int(ttl.Seconds())),
		"--weight", strconv.Itoa(weight),
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
		}
		return QuartermasterLease{}, &QuartermasterRefusalError{
			Pool:    req.Pool,
			Purpose: req.Purpose,
			Reason:  reason,
		}
	}
	values := parseQuartermasterLines(stdout.Bytes())
	account := values["ACCOUNT"]
	leaseID := values["LEASE_ID"]
	if account == "" || leaseID == "" {
		return QuartermasterLease{}, &QuartermasterRefusalError{
			Pool:    req.Pool,
			Purpose: req.Purpose,
			Reason:  "lease response did not name ACCOUNT and LEASE_ID",
		}
	}
	lease := QuartermasterLease{Account: account, ID: leaseID, Pool: req.Pool, Holder: req.Holder}
	if raw := values["EXPIRES_AT"]; raw != "" {
		if seconds, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
			lease.Until = time.Unix(seconds, 0)
		}
	}
	return lease, nil
}

func (c commandQuartermasterClient) Release(ctx context.Context, lease QuartermasterLease, outcome string) error {
	if lease.ID == "" {
		return nil
	}
	bin := c.Bin
	if strings.TrimSpace(bin) == "" {
		bin = defaultQuartermasterBin()
	}
	if outcome == "" {
		outcome = "completed"
	}
	holder := lease.Holder
	if holder == "" {
		holder = defaultQuartermasterHolder()
	}
	cmd := exec.CommandContext(ctx, bin, "release",
		lease.ID,
		"--holder", holder,
		"--outcome", outcome,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("release quartermaster lease %s: %w: %s", lease.ID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type quartermasterAgent struct {
	Agent
	opts QuartermasterOptions
}

func WithQuartermasterLease(a Agent, opts QuartermasterOptions) Agent {
	if a == nil || opts.Client == nil || strings.TrimSpace(opts.Pool) == "" {
		return a
	}
	if opts.TTL <= 0 {
		opts.TTL = defaultQuartermasterTTL
	}
	if opts.Weight <= 0 {
		opts.Weight = 1
	}
	if opts.Holder == "" {
		opts.Holder = defaultQuartermasterHolder()
	}
	return quartermasterAgent{Agent: a, opts: opts}
}

func (q quartermasterAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	purpose := opts.Purpose
	if purpose == "" {
		purpose = "agent"
	}
	lease, err := q.opts.Client.Acquire(ctx, QuartermasterAcquireRequest{
		Pool:    q.opts.Pool,
		Holder:  q.opts.Holder,
		Purpose: purpose,
		TTL:     q.opts.TTL,
		Weight:  q.opts.Weight,
	})
	if err != nil {
		return nil, err
	}
	boundEnv, err := quartermasterEnv(q.opts.Pool, lease, q.opts.Home)
	if err != nil {
		return nil, errors.Join(err, q.releaseError(lease, "cancelled"))
	}
	// Never append into the caller's backing array: RunOpts is copied by value
	// but its Env slice is not, so two invocations sharing one RunOpts would
	// bind each other's leased account home.
	env := make([]string, 0, len(opts.Env)+len(boundEnv))
	env = append(env, opts.Env...)
	opts.Env = append(env, boundEnv...)
	result, runErr := q.Agent.Run(ctx, opts)
	outcome := "completed"
	if runErr != nil {
		outcome = "failed"
		if ctx.Err() != nil {
			outcome = "cancelled"
		} else if outage, quota := lanehealth.Classify(q.opts.Pool, runErr.Error(), time.Now()); quota {
			runErr = &QuartermasterRefusalError{
				Pool:    q.opts.Pool,
				Purpose: purpose,
				Reason: fmt.Sprintf("leased account %s reported quota exhaustion until %s: %s",
					lease.Account,
					outage.Until.Local().Format(resetTimeLayout),
					outage.Reason),
			}
		}
	}
	if releaseErr := q.releaseError(lease, outcome); releaseErr != nil {
		return result, errors.Join(runErr, releaseErr)
	}
	return result, runErr
}

// releaseError returns the lease-release failure so the caller can surface it
// alongside whatever the invocation itself returned. A release that fails
// leaves the account leased until its TTL expires, so it must never be
// swallowed just because the run also failed.
func (q quartermasterAgent) releaseError(lease QuartermasterLease, outcome string) error {
	if err := q.opts.Client.Release(context.Background(), lease, outcome); err != nil {
		return fmt.Errorf("quartermaster lease %s was not released and stays held until its TTL expires: %w", lease.ID, err)
	}
	return nil
}

func (q quartermasterAgent) SupportsSessionResume() bool {
	return SupportsSessionResume(q.Agent)
}

func (q quartermasterAgent) SupportsSessionProvider(provider string) bool {
	return SupportsSessionProvider(q.Agent, provider)
}

func (q quartermasterAgent) ReportsAgentAttempts() bool {
	return ReportsAgentAttempts(q.Agent)
}

func (q quartermasterAgent) NeutralizesGateInstructions() bool {
	return NeutralizesGateInstructions(q.Agent)
}

func QuartermasterPoolForLane(lane string) (string, bool) {
	switch strings.TrimSpace(lane) {
	case "claude":
		return "claude", true
	case "codex":
		return "codex", true
	default:
		return "", false
	}
}

func quartermasterEnv(pool string, lease QuartermasterLease, home string) ([]string, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home for quartermaster lease: %w", err)
		}
	}
	env := []string{
		"NS_QUARTERMASTER_ACCOUNT=" + lease.Account,
		"NS_QUARTERMASTER_LEASE_ID=" + lease.ID,
		"NS_QUARTERMASTER_POOL=" + pool,
	}
	switch pool {
	case "claude":
		accountHome := quartermasterAccountHomeFromRegistry(pool, lease.Account)
		if accountHome == "" {
			accountHome = filepath.Join(home, ".claude-accounts", lease.Account)
		}
		if err := requireDir(accountHome); err != nil {
			return nil, &QuartermasterRefusalError{Pool: pool, Reason: err.Error()}
		}
		env = append(env, "CLAUDE_CONFIG_DIR="+accountHome)
	case "codex":
		accountHome := quartermasterAccountHomeFromRegistry(pool, lease.Account)
		if accountHome == "" {
			accountHome = filepath.Join(home, ".codex-accounts", lease.Account)
		}
		if err := requireDir(accountHome); err != nil {
			return nil, &QuartermasterRefusalError{Pool: pool, Reason: err.Error()}
		}
		env = append(env, "CODEX_HOME="+accountHome)
	}
	return env, nil
}

func quartermasterAccountHomeFromRegistry(pool, account string) string {
	path := os.Getenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".no-mistakes-dashboard", "execution-accounts.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var registry struct {
		Accounts []struct {
			Slug     string `json:"slug"`
			Provider string `json:"provider"`
			Home     string `json:"home"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return ""
	}
	for _, row := range registry.Accounts {
		if row.Slug == account && row.Home != "" && registryProviderMatches(pool, row.Provider) {
			return row.Home
		}
	}
	return ""
}

func registryProviderMatches(pool, provider string) bool {
	switch pool {
	case "claude":
		return provider == "claude" || provider == "anthropic"
	case "codex":
		return provider == "codex" || provider == "openai"
	default:
		return provider == pool
	}
}

func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("leased account home is unavailable: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("leased account home is not a directory: %s", path)
	}
	return nil
}

func parseQuartermasterLines(data []byte) map[string]string {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func defaultQuartermasterBin() string {
	if value := os.Getenv("NS_QUARTERMASTER_BIN"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "quartermaster.py"
	}
	return filepath.Join(home, ".fleet", "scripts", "quartermaster.py")
}

func defaultQuartermasterHolder() string {
	host, _ := os.Hostname()
	return quartermasterIdent("no-slop:"+host+":"+strconv.Itoa(os.Getpid()), time.Time{})
}

func quartermasterIdent(prefix string, t time.Time) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "no-slop"
	}
	var b strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == ':', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 96 {
			break
		}
	}
	out := strings.Trim(b.String(), ".:_-")
	if out == "" {
		out = "no-slop"
	}
	if t.IsZero() {
		if len(out) > 128 {
			return out[:128]
		}
		return out
	}
	suffix := "-" + strconv.FormatInt(t.UnixNano(), 36)
	if len(out)+len(suffix) > 128 {
		out = out[:128-len(suffix)]
		out = strings.Trim(out, ".:_-")
		if out == "" {
			out = "no-slop"
		}
	}
	return out + suffix
}

func IsQuartermasterRefusal(err error) bool {
	var refusal *QuartermasterRefusalError
	return errors.As(err, &refusal)
}
