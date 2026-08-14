package pathmatch_test

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/pathmatch"
)

func TestMatchSupportsRepositoryGlobsAndRecursiveDirectories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "outbound", pattern: "outbound/**", want: true},
		{name: "outbound/posts/note.md", pattern: "outbound/**", want: true},
		{name: "OUTBOUND/note.md", pattern: "outbound/*.md", want: true},
		{name: "notes/outbound.md", pattern: "outbound/**", want: false},
		{name: "docs/guide.md", pattern: "docs/*.md", want: true},
	}
	for _, test := range tests {
		if got := pathmatch.Match(test.name, test.pattern); got != test.want {
			t.Errorf("Match(%q, %q) = %t, want %t", test.name, test.pattern, got, test.want)
		}
	}
}
