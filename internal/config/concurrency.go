package config

import "fmt"

// Concurrency is host-daemon capacity, never contributor-controlled repo policy.
type Concurrency struct {
	Reviews int `yaml:"reviews"`
	Suites  int `yaml:"suites"`
}

type concurrencyRaw struct {
	Reviews *int `yaml:"reviews"`
	Suites  *int `yaml:"suites"`
}

// DefaultConcurrency limits executing reviews and suites independently.
// Suite commands may parallelize internally; these limits do not reserve CPUs.
func DefaultConcurrency() Concurrency { return Concurrency{Reviews: 9, Suites: 1} }

func resolveConcurrency(raw concurrencyRaw) (Concurrency, error) {
	c := DefaultConcurrency()
	for _, field := range []struct {
		name   string
		value  *int
		target *int
	}{
		{"reviews", raw.Reviews, &c.Reviews}, {"suites", raw.Suites, &c.Suites},
	} {
		if field.value == nil {
			continue
		}
		if *field.value <= 0 {
			return c, fmt.Errorf("concurrency.%s must be positive", field.name)
		}
		*field.target = *field.value
	}
	return c, nil
}
