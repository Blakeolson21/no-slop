package config

import (
	"fmt"
	"testing"
)

func TestTerminalConvergenceConfig(t *testing.T) {
	defaults := Merge(DefaultGlobalConfig(), &RepoConfig{}).Review.Convergence
	if defaults.MaxRounds != 5 || defaults.MaxRecurringRounds != 3 {
		t.Fatalf("defaults: %+v", defaults)
	}
	for _, key := range []string{"max_rounds", "max_recurring_rounds"} {
		if _, err := LoadRepoFromBytes([]byte("review:\n  convergence:\n    " + key + ": -1\n")); err == nil {
			t.Fatalf("accepted negative %s", key)
		}
	}
	for _, value := range []int{0, 8} {
		raw, err := LoadRepoFromBytes([]byte(fmt.Sprintf("review:\n  convergence:\n    max_rounds: %d\n    max_recurring_rounds: %d\n", value, value)))
		if err != nil {
			t.Fatal(err)
		}
		c := Merge(DefaultGlobalConfig(), raw).Review.Convergence
		if c.MaxRounds != value || c.MaxRecurringRounds != value {
			t.Fatalf("override: %+v", c)
		}
		c = Merge(DefaultGlobalConfig(), EffectiveRepoConfig(raw, nil, false)).Review.Convergence
		if c.MaxRounds != 5 || c.MaxRecurringRounds != 3 {
			t.Fatalf("untrusted override: %+v", c)
		}
	}
}
