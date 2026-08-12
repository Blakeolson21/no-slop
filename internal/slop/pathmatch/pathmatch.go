// Package pathmatch owns repository-relative path pattern matching for NoSlop.
package pathmatch

import (
	"path"
	"path/filepath"
	"strings"
)

// Match reports whether a repository-relative path matches a configured glob.
// A trailing /** includes the named directory and every descendant.
func Match(name, pattern string) bool {
	name = normalize(name)
	pattern = normalize(strings.TrimSpace(pattern))
	if name == "" || pattern == "" {
		return false
	}
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return name == prefix || strings.HasPrefix(name, prefix+"/")
	}
	matched, _ := path.Match(pattern, name)
	return matched
}

func normalize(value string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.ToSlash(value)), "./")
}
