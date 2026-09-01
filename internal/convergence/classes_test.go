package convergence

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestClassLabelRequiresStrictMajority(t *testing.T) {
	c := &class{
		members: []map[string]struct{}{
			{"shared": {}, "alpha": {}},
			{"shared": {}, "beta": {}},
		},
		tokenCount: map[string]int{
			"shared": 2,
			"alpha":  1,
			"beta":   1,
		},
	}

	if got, want := c.label(0), "shared"; got != want {
		t.Fatalf("two-member class label = %q, want %q; one-member tokens are noise", got, want)
	}
}

func TestStemCollapsesBaseFormsWithInflections(t *testing.T) {
	tests := []struct {
		base       string
		inflection string
	}{
		{base: "close", inflection: "closes"},
		{base: "close", inflection: "closing"},
		{base: "become", inflection: "becoming"},
		{base: "parse", inflection: "parses"},
		{base: "parse", inflection: "parsing"},
		{base: "quote", inflection: "quoted"},
		{base: "quote", inflection: "quoting"},
		// A base form that already ends in the doubled consonant its own
		// inflections shed must shed it too, or "add" classes separately from
		// "adds", "added", and "adding".
		{base: "add", inflection: "adds"},
		{base: "add", inflection: "added"},
		{base: "add", inflection: "adding"},
		{base: "err", inflection: "errs"},
		{base: "err", inflection: "erred"},
		{base: "stop", inflection: "stops"},
		{base: "stop", inflection: "stopped"},
		{base: "stop", inflection: "stopping"},
		{base: "call", inflection: "calls"},
		{base: "call", inflection: "calling"},
	}

	for _, tt := range tests {
		t.Run(tt.base+"_"+tt.inflection, func(t *testing.T) {
			if got, want := stem(tt.inflection), stem(tt.base); got != want {
				t.Fatalf("stem(%q) = %q, want stem(%q) = %q", tt.inflection, got, tt.base, want)
			}
		})
	}
}

func TestFindingTokensStillDropsStopwordsBeforeStemming(t *testing.T) {
	tokens := findingTokens(types.Finding{Description: "have done the work"})
	if _, ok := tokens["hav"]; ok {
		t.Fatalf("stemming reintroduced stopword have as token hav: %#v", tokens)
	}
}
