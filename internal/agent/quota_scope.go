package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/pelletier/go-toml/v2"
)

// QuotaScope identifies the provider account and selected model whose quota
// was observed. AccountID is a digest of the resolved provider home so local
// state never persists an account path or its directory name.
type QuotaScope struct {
	Key       string
	AccountID string
	Model     string
	provider  string
	home      string
	homeEnv   string
}

// QuotaScopeReporter is implemented by adapters that can resolve an exact
// provider home and model for an invocation. Missing identity stays untracked
// rather than falling back to a lane-wide mark.
type QuotaScopeReporter interface {
	QuotaScope(RunOpts) (QuotaScope, bool)
}

// ResolveQuotaScope returns the account/model identity for a possibly wrapped
// agent. Wrappers that cannot report an exact identity fail open by returning
// false; callers must not substitute the lane name.
func ResolveQuotaScope(a Agent, opts RunOpts) (QuotaScope, bool) {
	reporter, ok := a.(QuotaScopeReporter)
	if !ok {
		return QuotaScope{}, false
	}
	scope, ok := reporter.QuotaScope(opts)
	if !ok {
		return QuotaScope{}, false
	}
	scope.Key = lanehealth.ScopeKey(scope.AccountID, scope.Model)
	return scope, scope.Key != ""
}

// QuotaProbeRunner makes one bounded, fresh-session request on the adapter's
// configured account and model. It deliberately bypasses normal retry loops.
type QuotaProbeRunner interface {
	RunQuotaProbe(ctx context.Context, opts RunOpts) (*Result, error)
}

const quotaProbePrompt = "This is an account and model availability check, not a task. Do not read or change files, run commands, or use tools. Reply with exactly one word: OK."

func quotaProbeOpts(opts RunOpts) RunOpts {
	if !opts.quotaProbePrepared {
		opts.Prompt = quotaProbePrompt
	}
	opts.JSONSchema = nil
	opts.OnChunk = nil
	opts.Session = nil
	opts.SessionFallback = false
	opts.SessionFallbackReason = ""
	opts.Purpose = "quota-probe"
	opts.Workload = nil
	opts.quotaProbePrepared = true
	return opts
}

func runQuotaProbeOnce(ctx context.Context, a Agent, opts RunOpts, run func(context.Context, RunOpts) (*Result, error)) (*Result, error) {
	if strings.TrimSpace(opts.quotaProbeModel) == "" {
		return nil, fmt.Errorf("quota probe requires a resolved model")
	}
	opts = withInvocationIdentity(opts, a)
	startedAt := time.Now()
	result, err := run(ctx, quotaProbeOpts(opts))
	emitAgentAttempt(opts, a.Name(), result, err, startedAt, time.Now())
	return result, err
}

// isolatedQuotaProbeOpts removes the requested task's working directory from a
// recovery probe while preserving the exact provider home resolved for its
// account/model scope. The temporary directory is empty and is removed by the
// caller after the bounded adapter invocation completes.
func isolatedQuotaProbeOpts(opts RunOpts, scope QuotaScope) (RunOpts, func(), error) {
	if scope.provider == "" || scope.home == "" || scope.homeEnv == "" {
		return RunOpts{}, nil, fmt.Errorf("quota scope has no resolved provider home")
	}
	dir, err := os.MkdirTemp("", "no-slop-quota-probe-")
	if err != nil {
		return RunOpts{}, nil, fmt.Errorf("create isolated quota probe directory: %w", err)
	}
	opts = quotaProbeOpts(opts)
	opts.CWD = dir
	opts.Env = setEnvValue(opts.Env, "PWD", dir)
	opts.Env = setEnvValue(opts.Env, scope.homeEnv, scope.home)
	opts.quotaProbeModel = scope.Model
	return opts, func() { _ = os.RemoveAll(dir) }, nil
}

func setEnvValue(entries []string, name, value string) []string {
	updated := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key != name {
			updated = append(updated, entry)
		}
	}
	return append(updated, name+"="+value)
}

func accountModelScope(provider, home, model, cwd string) (QuotaScope, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if (provider != "codex" && provider != "claude") || home == "" || model == "" {
		return QuotaScope{}, false
	}
	home = resolveQuotaHome(home, cwd)
	if runtime.GOOS == "windows" {
		home = strings.ToLower(home)
	}
	accountDigest := sha256.Sum256([]byte("no-slop-lane-health-account-v1\x00" + provider + "\x00" + home))
	accountID := hex.EncodeToString(accountDigest[:])
	return QuotaScope{
		Key:       lanehealth.ScopeKey(accountID, model),
		AccountID: accountID,
		Model:     model,
		provider:  provider,
		home:      home,
		homeEnv:   providerHomeEnvironment(provider),
	}, true
}

func nativeQuotaScope(configuredProvider, bin string, args []string, opts RunOpts) (QuotaScope, bool) {
	provider, resolved := nativeProviderForExecutable(configuredProvider, bin)
	if !resolved {
		return QuotaScope{}, false
	}

	homeVariable := providerHomeEnvironment(provider)
	home, set := effectiveEnvValue(opts.Env, homeVariable)
	if !set || home == "" {
		userHome, ok := effectiveEnvValue(opts.Env, "HOME")
		if !ok || userHome == "" {
			userHome, _ = os.UserHomeDir()
		}
		if userHome == "" {
			return QuotaScope{}, false
		}
		if provider == "codex" {
			home = filepath.Join(userHome, ".codex")
		} else {
			home = filepath.Join(userHome, ".claude")
		}
	}
	home = resolveQuotaHome(home, opts.CWD)

	model, explicit := modelFromArgs(args)
	if !explicit {
		if provider == "claude" {
			model, explicit = effectiveEnvValue(opts.Env, "ANTHROPIC_MODEL")
			if explicit && model == "" {
				explicit = false
			}
		}
	}
	if !explicit {
		if provider == "codex" {
			model = codexConfiguredModel(home, args)
		} else {
			model = claudeConfiguredModel(home)
		}
	}
	return accountModelScope(provider, home, model, opts.CWD)
}

func nativeProviderForExecutable(configuredProvider, bin string) (string, bool) {
	configuredProvider = strings.ToLower(strings.TrimSpace(configuredProvider))
	if configuredProvider != "codex" && configuredProvider != "claude" {
		return "", false
	}
	identity := nativeInvocationIdentity(configuredProvider, bin, nil)
	if identity.Executable != nil {
		// The resolved executable is authoritative when available. In
		// particular, an adapter configured as Claude can be overridden to a
		// Codex binary; using Agent.Name or inherited home variables would then
		// mark the wrong account.
		if provider, ok := nativeProviderFromExecutableName(filepath.Base(*identity.Executable)); ok {
			return provider, true
		}
		// Some vendor installers symlink a canonical command name to a
		// versioned executable whose basename carries no provider identity.
		// Keep the launcher name only when the resolved target is not itself
		// recognizable as the other supported provider.
		return nativeProviderFromExecutableName(filepath.Base(bin))
	}
	// Preserve the canonical command identity when the command is not
	// currently on PATH. A missing executable cannot successfully produce a
	// quota banner, while custom unresolved commands remain untracked.
	return nativeProviderFromExecutableName(filepath.Base(bin))
}

func nativeProviderFromExecutableName(name string) (string, bool) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), filepath.Ext(name))
	for _, provider := range []string{"codex", "claude"} {
		if name == provider || strings.HasPrefix(name, provider+"-") || strings.HasPrefix(name, provider+"_") {
			return provider, true
		}
	}
	return "", false
}

func providerHomeEnvironment(provider string) string {
	if provider == "codex" {
		return "CODEX_HOME"
	}
	if provider == "claude" {
		return "CLAUDE_CONFIG_DIR"
	}
	return ""
}

// resolveQuotaHome matches the directory the adapter runs from when a provider
// home is relative, and canonicalizes aliases before config lookup and hashing.
func resolveQuotaHome(home, cwd string) string {
	if !filepath.IsAbs(home) {
		base := cwd
		if base == "" {
			base, _ = os.Getwd()
		}
		home = filepath.Join(base, home)
	}
	if absolute, err := filepath.Abs(home); err == nil {
		home = absolute
	}
	home = filepath.Clean(home)
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	return home
}

func effectiveEnvValue(extra []string, name string) (string, bool) {
	for i := len(extra) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(extra[i], "=")
		if ok && key == name {
			return value, true
		}
	}
	return os.LookupEnv(name)
}

func modelFromArgs(args []string) (string, bool) {
	model := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--model" || arg == "-m":
			if i+1 < len(args) {
				model = strings.TrimSpace(args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--model="):
			model = strings.TrimSpace(strings.TrimPrefix(arg, "--model="))
		case strings.HasPrefix(arg, "-m="):
			model = strings.TrimSpace(strings.TrimPrefix(arg, "-m="))
		case arg == "--profile":
			if i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(arg, "--profile="):
			// The selected profile's effective model is resolved from config.toml.
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				if value, ok := modelConfigOverride(args[i+1]); ok {
					model = value
				}
				i++
			}
		case strings.HasPrefix(arg, "--config="):
			if value, ok := modelConfigOverride(strings.TrimPrefix(arg, "--config=")); ok {
				model = value
			}
		}
	}
	if model != "" {
		return model, true
	}
	return "", false
}

func modelConfigOverride(value string) (string, bool) {
	key, raw, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(key) != "model" {
		return "", false
	}
	return parseConfigString(raw)
}

func codexConfiguredModel(home string, args []string) string {
	profile, profileSelected := codexProfileArg(args)
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return ""
	}
	var config struct {
		Model    string `toml:"model"`
		Profile  string `toml:"profile"`
		Profiles map[string]struct {
			Model string `toml:"model"`
		} `toml:"profiles"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return ""
	}
	if !profileSelected {
		profile = config.Profile
	}
	if selected, ok := config.Profiles[profile]; ok && strings.TrimSpace(selected.Model) != "" {
		return strings.TrimSpace(selected.Model)
	}
	return strings.TrimSpace(config.Model)
}

func codexProfileArg(args []string) (string, bool) {
	profile := ""
	selected := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--profile" && i+1 < len(args):
			profile = args[i+1]
			selected = true
			i++
		case strings.HasPrefix(args[i], "--profile="):
			profile = strings.TrimPrefix(args[i], "--profile=")
			selected = true
		}
	}
	return profile, selected
}

func claudeConfiguredModel(home string) string {
	for _, name := range []string{"settings.local.json", "settings.json"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			continue
		}
		var settings struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(data, &settings) == nil && strings.TrimSpace(settings.Model) != "" {
			return strings.TrimSpace(settings.Model)
		}
	}
	return ""
}

func parseConfigString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, `"`) {
		value, err := strconv.Unquote(raw)
		return strings.TrimSpace(value), err == nil && strings.TrimSpace(value) != ""
	}
	if strings.HasPrefix(raw, "'") {
		end := strings.LastIndex(raw[1:], "'")
		if end < 0 {
			return "", false
		}
		value := raw[1 : end+1]
		return strings.TrimSpace(value), strings.TrimSpace(value) != ""
	}
	if comment := strings.IndexByte(raw, '#'); comment >= 0 {
		raw = strings.TrimSpace(raw[:comment])
	}
	return raw, raw != ""
}
