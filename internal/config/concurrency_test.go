package config

import "testing"

func TestConcurrencyGlobalOnlyAndValidated(t *testing.T) {
	cfg, err := LoadGlobalFromBytes([]byte("concurrency:\n  reviews: 13\n  suites: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := LoadRepoFromBytes([]byte("concurrency: {reviews: 99, suites: 99}"))
	if err != nil {
		t.Fatal(err)
	}
	got := Merge(cfg, repo).Concurrency
	if got.Reviews != 13 || got.Suites != 2 {
		t.Fatalf("merged capacity: %+v", got)
	}
	for _, value := range []string{"reviews: 0", "reviews: -1", "suites: 0", "suites: -1"} {
		if _, err := LoadGlobalFromBytes([]byte("concurrency: {" + value + "}")); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	defaults, err := LoadGlobalFromBytes([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Concurrency != DefaultConcurrency() {
		t.Fatalf("defaults: %+v", defaults.Concurrency)
	}
}
