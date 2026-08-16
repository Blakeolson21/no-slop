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
	// Binary says git treats this blob as binary, so Content is raw bytes
	// rather than a diff. It is scanned through the binary-safe renderings
	// below rather than skipped: prepending one NUL byte to a plain text file
	// was enough to turn the mandatory leak scan into a check that printed
	// "completed (0 findings)" over a live AWS key and passed at exit 0.
	Binary bool
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

// binaryRenderings turns a blob git calls binary into text the line scanner can
// read. Two renderings are needed and both are cheap.
//
// The first replaces control bytes with spaces, which preserves every line
// boundary and recovers a plain text file carrying one stray NUL, the exact
// shape that bought a pass. The second removes NUL bytes outright, which
// recovers UTF-16-style content whose every second byte is a NUL and which the
// first rendering would have spaced into nonsense.
//
// This is best effort by construction, and the limit is stated rather than
// papered over: a credential a change deliberately encodes, in binary or in
// text, can still be shaped so no pattern here matches it. What the renderings
// remove is the cheap version, where the bytes are already in the clear and one
// control character was doing all the hiding.
func binaryRenderings(content string) []string {
	spaced := make([]byte, 0, len(content))
	stripped := make([]byte, 0, len(content))
	for index := 0; index < len(content); index++ {
		current := content[index]
		switch {
		case current == '\n' || current == '\t' || current == '\r':
			spaced = append(spaced, current)
			stripped = append(stripped, current)
		case current == 0:
			spaced = append(spaced, ' ')
		case current < 0x20 || current == 0x7f:
			spaced = append(spaced, ' ')
			stripped = append(stripped, ' ')
		default:
			spaced = append(spaced, current)
			stripped = append(stripped, current)
		}
	}
	renderings := []string{string(spaced)}
	if text := string(stripped); text != renderings[0] {
		renderings = append(renderings, text)
	}
	return renderings
}

// Scan checks text for secret shapes and private identity markers.
func Scan(files []File, opts Options) Result {
	var result Result
	blocklist := append(DefaultBlocklist(), opts.Blocklist...)
	for _, file := range files {
		renderings := []string{file.Content}
		if file.Binary {
			renderings = binaryRenderings(file.Content)
		}
		// One blob can be read more than one way, so the same credential can
		// surface twice. Findings and exemptions are keyed on what a reader
		// would act on, which is the line and the reason, not the rendering.
		reportedFinding := make(map[string]bool)
		reportedExemption := make(map[int]bool)
		for _, rendering := range renderings {
			for index, line := range strings.Split(rendering, "\n") {
				number := index + 1
				exempt := false
				if exemptionMarker.MatchString(line) {
					if opts.RefuseExemptions {
						disabled := Finding{
							Kind:        Secret,
							Path:        file.Path,
							Line:        number,
							Description: fmt.Sprintf("inline leak exemption %s is disabled by configuration", InlineExemption),
						}
						if key := findingKey(disabled); !reportedFinding[key] {
							reportedFinding[key] = true
							result.Findings = append(result.Findings, disabled)
						}
					} else {
						exempt = true
					}
				}
				lineFindings := scanLine(file.Path, number, line, blocklist)
				if exempt {
					if !reportedExemption[number] {
						reportedExemption[number] = true
						result.Exemptions = append(result.Exemptions, Exemption{
							Path:       file.Path,
							Line:       number,
							Marker:     InlineExemption,
							Suppressed: len(lineFindings),
						})
					}
					continue
				}
				for _, finding := range lineFindings {
					key := findingKey(finding)
					if reportedFinding[key] {
						continue
					}
					reportedFinding[key] = true
					result.Findings = append(result.Findings, finding)
				}
			}
		}
	}
	return result
}

func findingKey(finding Finding) string {
	return fmt.Sprintf("%d\x00%s", finding.Line, finding.Description)
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
