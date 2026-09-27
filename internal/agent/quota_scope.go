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
	probeArgs []string
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
	opts.quotaProbeArgs = append([]string(nil), scope.probeArgs...)
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
	scope, ok := accountModelScope(provider, home, model, opts.CWD)
	if ok && provider == "codex" {
		scope.probeArgs = codexQuotaProbeConfigArgs(home, args)
	}
	return scope, ok
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
		case arg == "--profile" || arg == "-p":
			if i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(arg, "--profile="), strings.HasPrefix(arg, "-p="):
			// The selected profile's effective model is resolved from config.toml.
		case strings.HasPrefix(arg, "-p") && len(arg) > 2:
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
	return codexEffectiveQuotaConfig(home, args).Model
}

func codexProfileArg(args []string) (string, bool) {
	profile := ""
	selected := false
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--profile" || args[i] == "-p") && i+1 < len(args):
			profile = args[i+1]
			selected = true
			i++
		case strings.HasPrefix(args[i], "--profile="), strings.HasPrefix(args[i], "-p="):
			profile = strings.TrimPrefix(args[i], "--profile=")
			if strings.HasPrefix(args[i], "-p=") {
				profile = strings.TrimPrefix(args[i], "-p=")
			}
			selected = true
		case strings.HasPrefix(args[i], "-p") && len(args[i]) > 2:
			profile = strings.TrimPrefix(args[i], "-p")
			profile = strings.TrimPrefix(profile, "=")
			selected = true
		}
	}
	return profile, selected
}

type codexQuotaConfig struct {
	Model          string                            `toml:"model"`
	Profile        string                            `toml:"profile"`
	ModelProvider  string                            `toml:"model_provider"`
	OpenAIBaseURL  string                            `toml:"openai_base_url"`
	OSSProvider    string                            `toml:"oss_provider"`
	Profiles       map[string]codexQuotaProfile      `toml:"profiles"`
	ModelProviders map[string]codexModelProviderConf `toml:"model_providers"`
}

type codexQuotaProfile struct {
	Model          string                            `toml:"model"`
	ModelProvider  string                            `toml:"model_provider"`
	OpenAIBaseURL  string                            `toml:"openai_base_url"`
	OSSProvider    string                            `toml:"oss_provider"`
	ModelProviders map[string]codexModelProviderConf `toml:"model_providers"`
}

type codexModelProviderConf struct {
	BaseURL            string `toml:"base_url"`
	WireAPI            string `toml:"wire_api"`
	EnvKey             string `toml:"env_key"`
	RequiresOpenAIAuth *bool  `toml:"requires_openai_auth"`
}

func codexEffectiveQuotaConfig(home string, args []string) codexQuotaProfile {
	base := codexQuotaProfile{}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err == nil {
		var config codexQuotaConfig
		if toml.Unmarshal(data, &config) == nil {
			base = codexQuotaProfile{
				Model:          strings.TrimSpace(config.Model),
				ModelProvider:  strings.TrimSpace(config.ModelProvider),
				OpenAIBaseURL:  strings.TrimSpace(config.OpenAIBaseURL),
				OSSProvider:    strings.TrimSpace(config.OSSProvider),
				ModelProviders: config.ModelProviders,
			}
			profile, selected := codexProfileArg(args)
			if !selected {
				profile = strings.TrimSpace(config.Profile)
			}
			if profile != "" && safeCodexProfileName(profile) {
				if profileData, readErr := os.ReadFile(filepath.Join(home, profile+".config.toml")); readErr == nil {
					var selectedConfig codexQuotaConfig
					if toml.Unmarshal(profileData, &selectedConfig) == nil {
						base = mergeCodexQuotaConfig(base, codexQuotaProfile{
							Model:          strings.TrimSpace(selectedConfig.Model),
							ModelProvider:  strings.TrimSpace(selectedConfig.ModelProvider),
							OpenAIBaseURL:  strings.TrimSpace(selectedConfig.OpenAIBaseURL),
							OSSProvider:    strings.TrimSpace(selectedConfig.OSSProvider),
							ModelProviders: selectedConfig.ModelProviders,
						})
					}
				} else if selectedConfig, ok := config.Profiles[profile]; ok {
					base = mergeCodexQuotaConfig(base, selectedConfig)
				}
			}
		}
	}
	applyCodexQuotaConfigOverrides(&base, args)
	return base
}

func safeCodexProfileName(profile string) bool {
	return profile != "" && profile != "." && profile != ".." && filepath.Base(profile) == profile && !strings.ContainsRune(profile, 0)
}

func mergeCodexQuotaConfig(base, overlay codexQuotaProfile) codexQuotaProfile {
	if overlay.Model != "" {
		base.Model = overlay.Model
	}
	if overlay.ModelProvider != "" {
		base.ModelProvider = overlay.ModelProvider
	}
	if overlay.OpenAIBaseURL != "" {
		base.OpenAIBaseURL = overlay.OpenAIBaseURL
	}
	if overlay.OSSProvider != "" {
		base.OSSProvider = overlay.OSSProvider
	}
	if base.ModelProviders == nil && len(overlay.ModelProviders) > 0 {
		base.ModelProviders = make(map[string]codexModelProviderConf, len(overlay.ModelProviders))
	}
	for name, provider := range overlay.ModelProviders {
		base.ModelProviders[name] = mergeCodexModelProviderConfig(base.ModelProviders[name], provider)
	}
	return base
}

func mergeCodexModelProviderConfig(base, overlay codexModelProviderConf) codexModelProviderConf {
	if overlay.BaseURL != "" {
		base.BaseURL = overlay.BaseURL
	}
	if overlay.WireAPI != "" {
		base.WireAPI = overlay.WireAPI
	}
	if overlay.EnvKey != "" {
		base.EnvKey = overlay.EnvKey
	}
	if overlay.RequiresOpenAIAuth != nil {
		base.RequiresOpenAIAuth = overlay.RequiresOpenAIAuth
	}
	return base
}

func applyCodexQuotaConfigOverrides(config *codexQuotaProfile, args []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var override string
		switch {
		case (arg == "-c" || arg == "--config") && i+1 < len(args):
			i++
			override = args[i]
		case strings.HasPrefix(arg, "--config="):
			override = strings.TrimPrefix(arg, "--config=")
		default:
			continue
		}
		key, raw, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		value, valueOK := parseConfigString(raw)
		if !valueOK {
			continue
		}
		switch key {
		case "model_provider":
			config.ModelProvider = value
		case "openai_base_url":
			config.OpenAIBaseURL = value
		case "oss_provider":
			config.OSSProvider = value
		default:
			const prefix = "model_providers."
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			providerName, field, ok := strings.Cut(strings.TrimPrefix(key, prefix), ".")
			if !ok || providerName == "" {
				continue
			}
			provider := config.ModelProviders[providerName]
			switch field {
			case "base_url":
				provider.BaseURL = value
			case "wire_api":
				provider.WireAPI = value
			case "env_key":
				provider.EnvKey = value
			case "requires_openai_auth":
				parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
				if err == nil {
					provider.RequiresOpenAIAuth = &parsed
				}
			}
			if config.ModelProviders == nil {
				config.ModelProviders = make(map[string]codexModelProviderConf)
			}
			config.ModelProviders[providerName] = provider
		}
	}
}

// codexQuotaProbeConfigArgs reconstructs only the selected provider route from
// the account config. User config itself stays disabled during the probe, so
// MCP servers, hooks, instructions, and unrelated capabilities do not leak
// into this one-word availability check.
func codexQuotaProbeConfigArgs(home string, args []string) []string {
	config := codexEffectiveQuotaConfig(home, args)
	var probeArgs []string
	if codexArgsContain(args, "--oss") {
		probeArgs = append(probeArgs, "--oss")
	}
	if config.ModelProvider != "" {
		probeArgs = append(probeArgs, "-c", "model_provider="+strconv.Quote(config.ModelProvider))
	}
	if config.OpenAIBaseURL != "" {
		probeArgs = append(probeArgs, "-c", "openai_base_url="+strconv.Quote(config.OpenAIBaseURL))
	}
	if config.OSSProvider != "" {
		probeArgs = append(probeArgs, "-c", "oss_provider="+strconv.Quote(config.OSSProvider))
	}
	if provider, ok := config.ModelProviders[config.ModelProvider]; ok && config.ModelProvider != "" {
		prefix := "model_providers." + config.ModelProvider + "."
		if provider.BaseURL != "" {
			probeArgs = append(probeArgs, "-c", prefix+"base_url="+strconv.Quote(provider.BaseURL))
		}
		if provider.WireAPI != "" {
			probeArgs = append(probeArgs, "-c", prefix+"wire_api="+strconv.Quote(provider.WireAPI))
		}
		if provider.EnvKey != "" {
			probeArgs = append(probeArgs, "-c", prefix+"env_key="+strconv.Quote(provider.EnvKey))
		}
		if provider.RequiresOpenAIAuth != nil {
			probeArgs = append(probeArgs, "-c", prefix+"requires_openai_auth="+strconv.FormatBool(*provider.RequiresOpenAIAuth))
		}
	}
	return probeArgs
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
