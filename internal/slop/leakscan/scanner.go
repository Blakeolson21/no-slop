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
	Blocklist        []string
	RefuseExemptions bool
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

// Exemption identifies an inline marker honored by the scanner. Suppressed is
// how many findings the marker actually removed, which is not the same as how
// many markers were seen: a marker on a clean line suppresses nothing, and
// reporting it as a bypass gave a reviewer no way to size the real one.
type Exemption struct {
	Path       string
	Line       int
	Marker     string
	Suppressed int
}

// Result contains leak findings and every honored inline exemption.
type Result struct {
	Findings   []Finding
	Exemptions []Exemption
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

// exemptionMarker requires the marker to sit in a comment or at the end of the
// line rather than merely appear somewhere in it. Plain `strings.Contains`
// meant a sentence that quoted the marker exempted its own line, which is a
// bypass anybody could trip over by writing documentation about the feature.
var exemptionMarker = regexp.MustCompile(`(?i)(?:^|//|#|/\*|<!--|--|;|\s)\s*` + regexp.QuoteMeta(InlineExemption) + `\b`)

// Scan checks text for secret shapes and private identity markers.
func Scan(files []File, opts Options) Result {
	var result Result
	blocklist := append(DefaultBlocklist(), opts.Blocklist...)
	for _, file := range files {
		for index, line := range strings.Split(file.Content, "\n") {
			exempt := false
			if exemptionMarker.MatchString(line) {
				if opts.RefuseExemptions {
					result.Findings = append(result.Findings, Finding{
						Kind:        Secret,
						Path:        file.Path,
						Line:        index + 1,
						Description: fmt.Sprintf("inline leak exemption %s is disabled by configuration", InlineExemption),
					})
				} else {
					exempt = true
				}
			}
			lineFindings := scanLine(file.Path, index+1, line, blocklist)
			if exempt {
				result.Exemptions = append(result.Exemptions, Exemption{
					Path:       file.Path,
					Line:       index + 1,
					Marker:     InlineExemption,
					Suppressed: len(lineFindings),
				})
				continue
			}
			result.Findings = append(result.Findings, lineFindings...)
		}
	}
	return result
}

func scanLine(path string, lineNumber int, line string, blocklist []string) []Finding {
	var findings []Finding
	for _, pattern := range secretPatterns {
		if pattern.re.MatchString(line) {
			findings = append(findings, Finding{
				Kind:        Secret,
				Path:        path,
				Line:        lineNumber,
				Description: fmt.Sprintf("possible %s shape", pattern.name),
			})
		}
	}
	for _, pattern := range identityPatterns {
		if pattern.re.MatchString(line) {
			findings = append(findings, Finding{
				Kind:        Identity,
				Path:        path,
				Line:        lineNumber,
				Description: fmt.Sprintf("possible %s", pattern.name),
			})
		}
	}
	lower := strings.ToLower(line)
	for _, entry := range blocklist {
		if entry != "" && strings.Contains(lower, strings.ToLower(entry)) {
			findings = append(findings, Finding{
				Kind:        Identity,
				Path:        path,
				Line:        lineNumber,
				Description: "private name matches the configured identity blocklist",
			})
		}
	}
	return findings
}
