// Package leakscan detects credentials and private identity markers without
// copying matched values into its findings.
package leakscan

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind identifies a leak finding class.
type Kind string

const (
	Secret   Kind = "secret"
	Identity Kind = "identity"
)

// File is text to scan with its repository-relative path.
type File struct {
	Path    string
	Content string
}

// Options configures private-name matching.
type Options struct {
	Blocklist []string
}

// InlineExemption marks a source line whose credential-shaped or private-name
// literal is an intentional fixture. The exemption applies only to that line.
const InlineExemption = "noslop:allow-leak"

// Finding identifies a leak without retaining the matched value.
type Finding struct {
	Kind        Kind
	Path        string
	Line        int
	Description string
}

type secretPattern struct {
	name string
	re   *regexp.Regexp
}

var secretPatterns = []secretPattern{
	{name: "GitHub token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{name: "AWS access key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{name: "Slack token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`)},
	{name: "private key", re: regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{name: "credential assignment", re: regexp.MustCompile(`(?i)\b(?:api[_-]?key|api[_-]?token|access[_-]?token|secret|password|passwd)\b\s*[:=]\s*["']?[A-Za-z0-9_./+\-=]{24,}`)},
}

var identityPatterns = []secretPattern{
	{name: "personal home path", re: regexp.MustCompile(`(?:/(?:Users|home)/[A-Za-z0-9._-]+/|[A-Za-z]:\\Users\\[A-Za-z0-9._-]+\\)`)},
}

var defaultBlocklist = []string{
	"internal-host",    // noslop:allow-leak
	"private-codename", // noslop:allow-leak
	"secret-project",   // noslop:allow-leak
}

// DefaultBlocklist returns generic examples that catch placeholder private
// identities and show the expected entry shape.
func DefaultBlocklist() []string {
	return append([]string(nil), defaultBlocklist...)
}

// ParseBlocklist parses one private name per line. Blank lines and comments
// are ignored.
func ParseBlocklist(content string) []string {
	var entries []string
	for _, line := range strings.Split(content, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// Scan checks text for secret shapes and private identity markers.
func Scan(files []File, opts Options) []Finding {
	var findings []Finding
	blocklist := append(DefaultBlocklist(), opts.Blocklist...)
	for _, file := range files {
		for index, line := range strings.Split(file.Content, "\n") {
			if strings.Contains(strings.ToLower(line), InlineExemption) {
				continue
			}
			for _, pattern := range secretPatterns {
				if pattern.re.MatchString(line) {
					findings = append(findings, Finding{
						Kind:        Secret,
						Path:        file.Path,
						Line:        index + 1,
						Description: fmt.Sprintf("possible %s shape", pattern.name),
					})
				}
			}
			for _, pattern := range identityPatterns {
				if pattern.re.MatchString(line) {
					findings = append(findings, Finding{
						Kind:        Identity,
						Path:        file.Path,
						Line:        index + 1,
						Description: fmt.Sprintf("possible %s", pattern.name),
					})
				}
			}
			lower := strings.ToLower(line)
			for _, entry := range blocklist {
				if entry != "" && strings.Contains(lower, strings.ToLower(entry)) {
					findings = append(findings, Finding{
						Kind:        Identity,
						Path:        file.Path,
						Line:        index + 1,
						Description: "private name matches the configured identity blocklist",
					})
				}
			}
		}
	}
	return findings
}
