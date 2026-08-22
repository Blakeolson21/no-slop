package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeQuartermasterClient struct {
	lease    QuartermasterLease
	acquire  error
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
	return nil
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
