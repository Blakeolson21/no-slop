package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeQuartermasterClient struct {
	lease    QuartermasterLease
	acquire  error
	release  error
	released []string
	requests []QuartermasterAcquireRequest
}

func (c *fakeQuartermasterClient) Acquire(_ context.Context, req QuartermasterAcquireRequest) (QuartermasterLease, error) {
	c.requests = append(c.requests, req)
	if c.acquire != nil {
		return QuartermasterLease{}, c.acquire
	}
	lease := c.lease
	lease.Pool = req.Pool
	return lease, nil
}

func (c *fakeQuartermasterClient) Release(_ context.Context, lease QuartermasterLease, outcome string) error {
	c.released = append(c.released, lease.ID+":"+outcome)
	return c.release
}

func TestQuartermasterLeaseBindsCodexHomeFromTheLeasedSeat(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY", filepath.Join(root, "missing-registry.json"))
	mkdir(t, root+"/.codex-accounts/blakeolson46")
	client := &fakeQuartermasterClient{lease: QuartermasterLease{Account: "blakeolson46", ID: "lease-1"}}
	inner := &envCapturingAgent{name: "codex"}
	leased := WithQuartermasterLease(inner, QuartermasterOptions{
		Client: client,
		Pool:   "codex",
		Holder: "test-holder",
		Home:   root,
	})

	result, err := leased.Run(context.Background(), RunOpts{Purpose: "review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("result = %+v", result)
	}
	if got := envValue(inner.env, "CODEX_HOME"); got != root+"/.codex-accounts/blakeolson46" {
		t.Fatalf("CODEX_HOME = %q", got)
	}
	if got := envValue(inner.env, "NS_QUARTERMASTER_ACCOUNT"); got != "blakeolson46" {
		t.Fatalf("NS_QUARTERMASTER_ACCOUNT = %q", got)
	}
	if len(client.requests) != 1 || client.requests[0].Pool != "codex" || client.requests[0].Purpose != "review" {
		t.Fatalf("request = %+v", client.requests)
	}
	if got := strings.Join(client.released, ","); got != "lease-1:completed" {
		t.Fatalf("released = %q", got)
	}
}

func TestQuartermasterLeaseBindsCodexHomeFromGeneratedRegistry(t *testing.T) {
	root := t.TempDir()
	accountHome := filepath.Join(root, ".codex-accounts", "blakeolson46")
	mkdir(t, accountHome)
	registry := filepath.Join(root, "execution-accounts.json")
	if err := os.WriteFile(registry, []byte(`{"accounts":[{"slug":"codexblake46","provider":"codex","home":"`+accountHome+`"}]}`), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	t.Setenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY", registry)

	client := &fakeQuartermasterClient{lease: QuartermasterLease{Account: "codexblake46", ID: "lease-1"}}
	inner := &envCapturingAgent{name: "codex"}
	leased := WithQuartermasterLease(inner, QuartermasterOptions{
		Client: client,
		Pool:   "codex",
		Holder: "test-holder",
		Home:   root,
	})

	_, err := leased.Run(context.Background(), RunOpts{Purpose: "review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := envValue(inner.env, "CODEX_HOME"); got != accountHome {
		t.Fatalf("CODEX_HOME = %q", got)
	}
	if got := envValue(inner.env, "NS_QUARTERMASTER_ACCOUNT"); got != "codexblake46" {
		t.Fatalf("NS_QUARTERMASTER_ACCOUNT = %q", got)
	}
}

func TestQuartermasterRefusalStopsBeforeLaunchingAgent(t *testing.T) {
	client := &fakeQuartermasterClient{
		acquire: &QuartermasterRefusalError{Pool: "claude", Purpose: "review", Reason: "reserved lead seat"},
	}
	inner := &envCapturingAgent{name: "claude"}
	leased := WithQuartermasterLease(inner, QuartermasterOptions{
		Client: client,
		Pool:   "claude",
		Holder: "test-holder",
		Home:   t.TempDir(),
	})

	_, err := leased.Run(context.Background(), RunOpts{Purpose: "review"})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !IsQuartermasterRefusal(err) {
		t.Fatalf("error %T/%v must be a Quartermaster refusal", err, err)
	}
	if inner.calls != 0 {
		t.Fatalf("agent launched %d times after refusal, want 0", inner.calls)
	}
	if len(client.released) != 0 {
		t.Fatalf("refused lease must not release anything: %+v", client.released)
	}
}

func TestQuartermasterLeaseReleasesFailedRuns(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY", filepath.Join(root, "missing-registry.json"))
	mkdir(t, root+"/.claude-accounts/nova")
	client := &fakeQuartermasterClient{lease: QuartermasterLease{Account: "nova", ID: "lease-2"}}
	wantErr := errors.New("agent failed")
	inner := &envCapturingAgent{name: "claude", err: wantErr}
	leased := WithQuartermasterLease(inner, QuartermasterOptions{
		Client: client,
		Pool:   "claude",
		Holder: "test-holder",
		Home:   root,
		TTL:    time.Minute,
	})

	_, err := leased.Run(context.Background(), RunOpts{Purpose: "review-fix"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if got := envValue(inner.env, "CLAUDE_CONFIG_DIR"); got != root+"/.claude-accounts/nova" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", got)
	}
	if got := strings.Join(client.released, ","); got != "lease-2:failed" {
		t.Fatalf("released = %q", got)
	}
}

// A release that fails leaves the account leased until its TTL expires, so it
// must reach the operator even when the invocation itself also failed.
func TestQuartermasterLeaseSurfacesAReleaseFailureAlongsideTheRunFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY", filepath.Join(root, "missing-registry.json"))
	mkdir(t, root+"/.claude-accounts/nova")
	releaseErr := errors.New("quartermaster unreachable")
	client := &fakeQuartermasterClient{
		lease:   QuartermasterLease{Account: "nova", ID: "lease-3"},
		release: releaseErr,
	}
	runErr := errors.New("agent failed")
	leased := WithQuartermasterLease(&envCapturingAgent{name: "claude", err: runErr}, QuartermasterOptions{
		Client: client,
		Pool:   "claude",
		Holder: "test-holder",
		Home:   root,
	})

	_, err := leased.Run(context.Background(), RunOpts{Purpose: "review-fix"})
	if !errors.Is(err, runErr) {
		t.Fatalf("Run error %v must keep the invocation failure", err)
	}
	if !errors.Is(err, releaseErr) {
		t.Fatalf("Run error %v must also report the leaked lease", err)
	}
}

// The env binding is per-invocation, so two invocations that share one RunOpts
// must not bind each other's leased account home. RunOpts is copied by value
// but its Env slice is not, so appending in place cross-binds under load.
func TestQuartermasterLeaseBindsPerInvocationEnvUnderConcurrency(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_QUARTERMASTER_ACCOUNT_REGISTRY", filepath.Join(root, "missing-registry.json"))
	const lanes = 8
	for i := 0; i < lanes; i++ {
		mkdir(t, filepath.Join(root, ".codex-accounts", "seat-"+strconv.Itoa(i)))
	}

	shared := make([]string, 1, 64)
	shared[0] = "SHARED=1"

	var wg sync.WaitGroup
	bound := make([]string, lanes)
	released := make([]string, lanes)
	for i := 0; i < lanes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			account := "seat-" + strconv.Itoa(i)
			inner := &envCapturingAgent{name: "codex"}
			leased := WithQuartermasterLease(inner, QuartermasterOptions{
				Client: &concurrentQuartermasterClient{lease: QuartermasterLease{Account: account, ID: "lease-" + account}},
				Pool:   "codex",
				Holder: "test-holder",
				Home:   root,
			})
			if _, err := leased.Run(context.Background(), RunOpts{Purpose: "review", Env: shared}); err != nil {
				t.Errorf("Run %s: %v", account, err)
				return
			}
			bound[i] = envValue(inner.env, "CODEX_HOME")
			released[i] = envValue(inner.env, "NS_QUARTERMASTER_ACCOUNT")
		}(i)
	}
	wg.Wait()

	for i := 0; i < lanes; i++ {
		account := "seat-" + strconv.Itoa(i)
		if want := filepath.Join(root, ".codex-accounts", account); bound[i] != want {
			t.Fatalf("lane %d bound CODEX_HOME %q, want %q", i, bound[i], want)
		}
		if released[i] != account {
			t.Fatalf("lane %d bound account %q, want %q", i, released[i], account)
		}
	}
	if shared[0] != "SHARED=1" || len(shared) != 1 {
		t.Fatalf("caller env was mutated: %#v", shared)
	}
}

type concurrentQuartermasterClient struct {
	lease QuartermasterLease
}

func (c *concurrentQuartermasterClient) Acquire(_ context.Context, req QuartermasterAcquireRequest) (QuartermasterLease, error) {
	lease := c.lease
	lease.Pool = req.Pool
	return lease, nil
}

func (c *concurrentQuartermasterClient) Release(context.Context, QuartermasterLease, string) error {
	return nil
}

func TestCommandQuartermasterReleaseUsesLeaseIDPositionally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-only")
	}
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	bin := filepath.Join(root, "quartermaster")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + calls + `"
if [ "$1" = "acquire" ]; then
  printf 'ACCOUNT=codexblake46\nLEASE_ID=lease-123\nEXPIRES_AT=1787360823\n'
  exit 0
fi
if [ "$1" = "release" ] && [ "$2" = "lease-123" ] && [ "$3" = "--holder" ] && [ "$5" = "--outcome" ]; then
  exit 0
fi
printf 'bad args: %s\n' "$*" >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write quartermaster fixture: %v", err)
	}
	client := NewCommandQuartermasterClient(bin)
	lease, err := client.Acquire(context.Background(), QuartermasterAcquireRequest{
		Pool:    "codex",
		Holder:  "test-holder",
		Purpose: "review",
		TTL:     time.Minute,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lease.Holder = "test-holder"
	if err := client.Release(context.Background(), lease, "completed"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	if !strings.Contains(string(data), "release lease-123 --holder test-holder --outcome completed") {
		t.Fatalf("release call did not pass lease id positionally:\n%s", data)
	}
}

type envCapturingAgent struct {
	name  string
	env   []string
	calls int
	err   error
}

func (a *envCapturingAgent) Name() string { return a.name }

func (a *envCapturingAgent) Run(_ context.Context, opts RunOpts) (*Result, error) {
	a.calls++
	a.env = append([]string(nil), opts.Env...)
	if a.err != nil {
		return nil, a.err
	}
	return &Result{Text: "ok"}, nil
}

func (a *envCapturingAgent) Close() error { return nil }

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// A missing lease binary is not a policy decision. Reporting it as a bare
// refusal makes an operator-fixable infrastructure fault indistinguishable
// from "the pool is full", so the underlying failure must stay reachable.
func TestCommandQuartermasterAcquireKeepsTheUnderlyingSubprocessFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-quartermaster")

	_, err := NewCommandQuartermasterClient(missing).Acquire(context.Background(), QuartermasterAcquireRequest{
		Pool:    "codex",
		Holder:  "test-holder",
		Purpose: "review",
		TTL:     time.Minute,
	})
	if err == nil {
		t.Fatal("expected a missing lease binary to fail")
	}
	if !IsQuartermasterRefusal(err) {
		t.Fatalf("error %v must still classify as a quartermaster refusal", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error %v must keep the underlying subprocess failure reachable", err)
	}
	if strings.Contains(err.Error(), "no lease granted") {
		t.Fatalf("error %q reports an empty reason instead of naming the fault", err)
	}
}

// A caller deadline shorter than the client's own ceiling must surface as a
// timeout, not as an unexplained refusal.
func TestCommandQuartermasterAcquireSurfacesACallerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-only")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "quartermaster")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write quartermaster fixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewCommandQuartermasterClient(bin).Acquire(ctx, QuartermasterAcquireRequest{Pool: "codex", Holder: "test-holder"})
	if err == nil {
		t.Fatal("expected the caller deadline to fail the acquire")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v must report the deadline that stopped it", err)
	}
}

// A response naming a LEASE_ID but no ACCOUNT means a seat was granted that
// this process cannot use. Returning without handing it back holds the account
// for the whole TTL.
func TestCommandQuartermasterAcquireHandsBackASeatItCannotUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-only")
	}
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	bin := filepath.Join(root, "quartermaster")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + calls + `"
if [ "$1" = "acquire" ]; then
  printf 'LEASE_ID=lease-orphan\nEXPIRES_AT=1787360823\n'
fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write quartermaster fixture: %v", err)
	}

	lease, err := NewCommandQuartermasterClient(bin).Acquire(context.Background(), QuartermasterAcquireRequest{
		Pool:   "codex",
		Holder: "test-holder",
		TTL:    time.Minute,
	})
	if err == nil {
		t.Fatal("expected an incomplete lease response to fail")
	}
	if !IsQuartermasterRefusal(err) {
		t.Fatalf("error %v must classify as a quartermaster refusal", err)
	}
	if lease.ID != "" {
		t.Fatalf("returned lease = %+v, want no usable lease", lease)
	}
	data, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatalf("read calls: %v", readErr)
	}
	if !strings.Contains(string(data), "release lease-orphan") {
		t.Fatalf("the granted seat was never handed back; calls were:\n%s", data)
	}
}
