// Package identity owns the canonical no-slop names and their compatibility
// aliases. Keep identity resolution centralized so old and new spellings can
// never select different state silently.
package identity

import (
	"fmt"
	"os"
	"strings"
)

const (
	CommandName       = "no-slop"
	LegacyCommandName = "no-mistakes"
	ShortName         = "ns"
	LegacyShortName   = "nm"

	HomeEnv       = "NS_HOME"
	LegacyHomeEnv = "NM_HOME"

	AllowDefaultRootInTestsEnv       = "NS_ALLOW_DEFAULT_ROOT_IN_TESTS"
	LegacyAllowDefaultRootInTestsEnv = "NO_MISTAKES_ALLOW_DEFAULT_ROOT_IN_TESTS"

	RepoConfigName       = ".no-slop.yaml"
	LegacyRepoConfigName = ".no-mistakes.yaml"

	// The physical state directory deliberately remains at the legacy path for
	// this compatibility stage. A later, separately coordinated rollout owns
	// moving it.
	DefaultStateDir = ".no-mistakes"
)

// LookupEnv resolves one logical setting from its canonical and legacy names.
// Equal duplicate values are harmless. Different values are refused so aliases
// can never become two independent settings with an implicit precedence rule.
func LookupEnv(canonical, legacy string) (string, error) {
	canonicalValue := os.Getenv(canonical)
	legacyValue := os.Getenv(legacy)
	if canonicalValue != "" && legacyValue != "" && canonicalValue != legacyValue {
		return "", fmt.Errorf("%s and legacy alias %s configure the same setting with different values", canonical, legacy)
	}
	if canonicalValue != "" {
		return canonicalValue, nil
	}
	return legacyValue, nil
}

// LookupEnvSlice is LookupEnv for an explicit subprocess environment. A nil
// slice falls back to the process environment, matching os/exec conventions.
func LookupEnvSlice(env []string, canonical, legacy string) (string, error) {
	if env == nil {
		return LookupEnv(canonical, legacy)
	}
	values := make(map[string]string, 2)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == canonical || key == legacy) {
			values[key] = value
		}
	}
	if values[canonical] != "" && values[legacy] != "" && values[canonical] != values[legacy] {
		return "", fmt.Errorf("%s and legacy alias %s configure the same setting with different values", canonical, legacy)
	}
	if values[canonical] != "" {
		return values[canonical], nil
	}
	return values[legacy], nil
}

// EnvEnabled reports whether either spelling of a boolean marker is exactly 1.
// Conflicting non-empty spellings are rejected by LookupEnv.
func EnvEnabled(canonical, legacy string) (bool, error) {
	value, err := LookupEnv(canonical, legacy)
	return value == "1", err
}
