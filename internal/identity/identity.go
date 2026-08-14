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
	canonicalValue, canonicalSet := os.LookupEnv(canonical)
	legacyValue, legacySet := os.LookupEnv(legacy)
	return resolveAliasValues(canonical, legacy, canonicalValue, canonicalSet, legacyValue, legacySet)
}

// LookupEnvSlice is LookupEnv for an explicit subprocess environment. A nil
// slice falls back to the process environment, matching os/exec conventions.
func LookupEnvSlice(env []string, canonical, legacy string) (string, error) {
	if env == nil {
		return LookupEnv(canonical, legacy)
	}
	var canonicalValue, legacyValue string
	var canonicalSet, legacySet bool
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case canonical:
			canonicalValue = value
			canonicalSet = true
		case legacy:
			legacyValue = value
			legacySet = true
		}
	}
	return resolveAliasValues(canonical, legacy, canonicalValue, canonicalSet, legacyValue, legacySet)
}

func resolveAliasValues(canonical, legacy, canonicalValue string, canonicalSet bool, legacyValue string, legacySet bool) (string, error) {
	if canonicalSet && legacySet && canonicalValue != legacyValue {
		return "", fmt.Errorf("%s and legacy alias %s configure the same setting with different values", canonical, legacy)
	}
	if canonicalSet {
		return canonicalValue, nil
	}
	return legacyValue, nil
}

// EnvEnabled reports whether either spelling of a boolean marker is exactly 1.
// Conflicting spellings are rejected by LookupEnv.
func EnvEnabled(canonical, legacy string) (bool, error) {
	value, err := LookupEnv(canonical, legacy)
	return value == "1", err
}
