// Package pathmatch owns repository-relative path pattern matching for NoSlop.
package pathmatch

import (
	"path"
	"path/filepath"
	"strings"
)

// Match reports whether a repository-relative path matches a configured glob.
//
// A `**` segment matches any number of path segments, including none, so
// `**/AGENTS.md` matches `AGENTS.md` and `services/api/AGENTS.md` alike and a
// trailing `/**` includes the named directory and every descendant. A `*`
// inside one segment never crosses a slash. Supporting `**` at only one
// position was the shape that let an operator configure a protection that
// silently matched nothing.
func Match(name, pattern string) bool {
	name = normalize(name)
	pattern = normalize(strings.TrimSpace(pattern))
	if name == "" || pattern == "" {
		return false
	}
	return matchSegments(strings.Split(name, "/"), strings.Split(pattern, "/"))
}

// MatchesAny reports whether the path matches any of the configured patterns.
func MatchesAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if Match(name, pattern) {
			return true
		}
	}
	return false
}

func matchSegments(name, pattern []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			pattern = pattern[1:]
			if len(pattern) == 0 {
				return true
			}
			for skipped := 0; skipped <= len(name); skipped++ {
				if matchSegments(name[skipped:], pattern) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		matched, err := path.Match(pattern[0], name[0])
		if err != nil || !matched {
			return false
		}
		name = name[1:]
		pattern = pattern[1:]
	}
	return len(name) == 0
}

func normalize(value string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.ToSlash(value)), "./")
}
