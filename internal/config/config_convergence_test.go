package config

import (
	"strings"
	"testing"
)

func TestReviewConvergenceDefaults(t *testing.T) {
	cfg := Merge(DefaultGlobalConfig(), &RepoConfig{})
	c := cfg.Review.Convergence
	if c.NonDecreasingRounds != DefaultReviewConvergenceNonDecreasingRounds ||
		c.RecurringRounds != DefaultReviewConvergenceRecurringRounds ||
		c.BudgetMinutes != DefaultReviewConvergenceBudgetMinutes {
		t.Fatalf("convergence defaults = %+v", c)
	}
	if c.NonDecreasingRounds != 3 || c.RecurringRounds != 3 || c.BudgetMinutes != 120 {
		t.Fatalf("documented defaults changed without updating docs: %+v", c)
	}
}

func TestReviewConvergenceOverridesFromRepoConfig(t *testing.T) {
	repo, err := LoadRepoFromBytes([]byte(`
review:
  convergence:
    non_decreasing_rounds: 5
    recurring_rounds: 0
    budget_minutes: 30
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := Merge(DefaultGlobalConfig(), repo)
	c := cfg.Review.Convergence
	if c.NonDecreasingRounds != 5 || c.RecurringRounds != 0 || c.BudgetMinutes != 30 {
		t.Fatalf("convergence overrides = %+v", c)
	}
}

func TestReviewConvergencePartialOverrideKeepsOtherDefaults(t *testing.T) {
	repo, err := LoadRepoFromBytes([]byte(`
review:
  convergence:
    budget_minutes: 0
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := Merge(DefaultGlobalConfig(), repo)
	c := cfg.Review.Convergence
	if c.BudgetMinutes != 0 {
		t.Fatalf("explicit zero should disable the budget, got %d", c.BudgetMinutes)
	}
	if c.NonDecreasingRounds != 3 || c.RecurringRounds != 3 {
		t.Fatalf("unset thresholds should keep defaults, got %+v", c)
	}
}

func TestReviewConvergenceRejectsNegativeValues(t *testing.T) {
	for _, key := range []string{"non_decreasing_rounds", "recurring_rounds", "budget_minutes"} {
		_, err := LoadRepoFromBytes([]byte("review:\n  convergence:\n    " + key + ": -1\n"))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("negative %s should fail config parsing, got %v", key, err)
		}
	}
}

// The convergence thresholds ride the Review section, which is trusted-only: a
// pushed branch must not be able to widen or disable the guard on its own run.
func TestEffectiveRepoConfig_ReviewConvergenceTrustedOnly(t *testing.T) {
	pushed, err := LoadRepoFromBytes([]byte(`
review:
  convergence:
    non_decreasing_rounds: 0
    recurring_rounds: 0
    budget_minutes: 0
`))
	if err != nil {
		t.Fatalf("parse pushed: %v", err)
	}
	trusted, err := LoadRepoFromBytes([]byte(`
review:
  convergence:
    budget_minutes: 45
`))
	if err != nil {
		t.Fatalf("parse trusted: %v", err)
	}

	effective := EffectiveRepoConfig(pushed, trusted, false)
	cfg := Merge(DefaultGlobalConfig(), effective)
	c := cfg.Review.Convergence
	if c.NonDecreasingRounds != 3 || c.RecurringRounds != 3 || c.BudgetMinutes != 45 {
		t.Fatalf("pushed branch influenced trusted convergence thresholds: %+v", c)
	}

	// No trusted copy: the pushed block is discarded entirely and defaults apply.
	effective = EffectiveRepoConfig(pushed, nil, false)
	cfg = Merge(DefaultGlobalConfig(), effective)
	c = cfg.Review.Convergence
	if c.NonDecreasingRounds != 3 || c.RecurringRounds != 3 || c.BudgetMinutes != 120 {
		t.Fatalf("pushed convergence block survived without a trusted copy: %+v", c)
	}
}
