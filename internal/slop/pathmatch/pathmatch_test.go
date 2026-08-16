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

// TestMatchTreatsDoubleStarAsAnyDepth pins the globstar idiom an operator
// reaches for when they mean "this file wherever it lives". Before this, every
// one of these returned false and the configured protection covered nothing,
// with no warning that the pattern had matched no path.
func TestMatchTreatsDoubleStarAsAnyDepth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "AGENTS.md", pattern: "**/AGENTS.md", want: true},
		{name: "services/api/AGENTS.md", pattern: "**/AGENTS.md", want: true},
		{name: "skills/onboarding/SKILL.md", pattern: "**/SKILL.md", want: true},
		{name: "SKILL.md", pattern: "**/SKILL.md", want: true},
		{name: "docs/deep/nested/guide.md", pattern: "**/*.md", want: true},
		{name: "internal/auth/policy.go", pattern: "internal/**/policy.go", want: true},
		{name: "internal/policy.go", pattern: "internal/**/policy.go", want: true},
		{name: "internal/auth/deep/policy.go", pattern: "internal/**/policy.go", want: true},
		{name: "other/auth/policy.go", pattern: "internal/**/policy.go", want: false},
		{name: "AGENTS.md.bak", pattern: "**/AGENTS.md", want: false},
		{name: "notes/agentsxmd", pattern: "**/AGENTS.md", want: false},
		{name: "anything/at/all", pattern: "**", want: true},
	}
	for _, test := range tests {
		if got := pathmatch.Match(test.name, test.pattern); got != test.want {
			t.Errorf("Match(%q, %q) = %t, want %t", test.name, test.pattern, got, test.want)
		}
	}
}

// TestMatchKeepsSingleStarInsideOneSegment guards the other half: widening `**`
// must not widen `*` into a separator-crossing wildcard.
func TestMatchKeepsSingleStarInsideOneSegment(t *testing.T) {
	t.Parallel()

	if pathmatch.Match("docs/deep/guide.md", "docs/*.md") {
		t.Error("docs/*.md must not cross a slash")
	}
	if !pathmatch.Match("guide.md", "*.md") {
		t.Error("*.md must still match at the repository root")
	}
	if pathmatch.Match("docs/guide.md", "*.md") {
		t.Error("*.md must not match a nested path")
	}
}
