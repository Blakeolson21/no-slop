// Package leakscan detects credentials and private identity markers without
// copying matched values into its findings.
package leakscan

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
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
	// Binary says Content is a raw blob rather than diff text, so it is read
	// through the binary-safe renderings below rather than line by line. The
	// caller sets it from IsBinaryContent over the same bytes, never from git's
	// rendering: prepending one NUL byte to a plain text file was enough to
	// turn the mandatory leak scan into a check that printed "completed (0
	// findings)" over a live AWS key and passed at exit 0, and moving the NUL
	// past git's 8000-byte sniff window reopened it after the first fix.
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

// exemptionMarker requires the marker to open a comment or the line rather
// than merely appear somewhere in it. Plain `strings.Contains` meant a sentence
// that quoted the marker exempted its own line, which is a bypass anybody could
// trip over by writing documentation about the feature.
//
// The alternatives are comment introducers plus start-of-line. A bare
// whitespace alternative was there too, which reopened the exact case the rest
// of the pattern closes: a marker is always preceded by whitespace in a
// sentence, so `write noslop:allow-leak beside the key` matched and suppressed
// the credential on its own line. Every legitimate placement is still covered,
// because `\s*` after an introducer absorbs the space in
// `key = "..." # noslop:allow-leak` and after `^` it absorbs indentation.
//
// The guarantee is bounded to that: a marker inside a sentence does not exempt.
// It is not that no prose can ever match. Two introducers double as ordinary
// punctuation, so `the key is ignored -- noslop:allow-leak marks it` and the
// same sentence with a semicolon both fire, as does any line whose first
// non-space token is the marker. Tightening further would cost real markers in
// languages that use those introducers, and the residue is bounded because
// exemptions are off by default, a match exempts only its own line, and every
// honored marker is reported with the count it suppressed.
var exemptionMarker = regexp.MustCompile(`(?i)(?:^|//|#|/\*|<!--|--|;)\s*` + regexp.QuoteMeta(InlineExemption) + `\b`)

// IsBinaryContent reports whether these bytes have to be read through the
// binary-safe renderings rather than as text.
//
// The decision belongs to the scanner and is taken from the bytes the scanner
// is holding. It is deliberately not git's, and it deliberately does not look
// at how the content arrived. Git samples the first 8000 bytes of a blob to
// decide whether to render a diff at all, and the engine's fallback keyed on
// whether hunks appeared, so both agreed that a plain text file carrying one
// NUL past offset 8000 was ordinary text: the renderings never ran, the
// credential regex failed across the NUL, and a live AWS key reached "leak scan
// completed (0 findings)" at exit 0. An uncommitted `.git/info/attributes`
// holding `* diff` reached the same state from the opposite direction, forcing
// a text hunk over a NUL blob from a file that never appears in the commit and
// does not show in `git status`.
//
// So there is no sampling window and no consultation of git's rendering. A NUL
// anywhere, or any other C0 control byte that is not ordinary whitespace, means
// the binary-safe renderings run. The cost is that a file carrying a stray
// control byte is scanned whole rather than by diff hunk, which over-reports on
// that one path and is the correct direction.
func IsBinaryContent(content string) bool {
	for index := 0; index < len(content); index++ {
		switch current := content[index]; current {
		case '\n', '\t', '\r', '\f', '\v':
			continue
		default:
			if current == 0 || current < 0x20 || current == 0x7f {
				return true
			}
		}
	}
	return false
}

// HasInvisibleRunes reports whether these bytes carry a character that renders
// as nothing a reviewer can see.
//
// It is deliberately a separate question from IsBinaryContent. Round 4 keyed
// the binary decision on the C0 range and closed both NUL shapes; round 5
// walked around it with U+200B and U+0085, which are text by that decision and
// break the credential regex just as completely. The obvious next move was to
// widen the byte range again, and it is the move this family has already made
// four times: whatever class the code names, the character just outside it is
// the next probe.
//
// So this does not decide binary-vs-text at all. Making a byte order mark mean
// "binary" would report reduced coverage on ordinary text files, which is the
// same dishonest check line in the other direction. It decides only whether
// there is a SECOND reading of these bytes worth scanning, and Scan then scans
// both. A credential now has to survive the file as written and the file with
// its invisible characters removed.
//
// The class is Unicode's own answer to "renders as nothing": format characters
// (Cf, which covers U+200B, U+FEFF, U+00AD, the bidi marks and isolates, and
// the word joiner), the C1 controls including U+0085, and the line and
// paragraph separators U+2028 and U+2029. Ordinary whitespace is excluded
// because it is visible in its effect and is what line structure is made of.
func HasInvisibleRunes(content string) bool {
	for _, current := range content {
		if invisibleRune(current) {
			return true
		}
	}
	return false
}

func invisibleRune(value rune) bool {
	switch value {
	case '\n', '\t', '\r', '\f', '\v', ' ':
		return false
	}
	return unicode.Is(unicode.Cf, value) ||
		unicode.Is(unicode.Cc, value) ||
		unicode.Is(unicode.Zl, value) ||
		unicode.Is(unicode.Zp, value)
}

// withoutInvisibleRunes is the second reading: the same bytes with everything
// that renders as nothing taken out, so a pattern broken only by an invisible
// character matches again. The characters are removed rather than replaced with
// a space, because a space would break the credential just as well.
func withoutInvisibleRunes(content string) string {
	var out strings.Builder
	out.Grow(len(content))
	for _, current := range content {
		if invisibleRune(current) {
			continue
		}
		out.WriteRune(current)
	}
	return out.String()
}

// binaryRenderings turns a blob the scanner calls binary into text the line
// scanner can read. Two renderings are needed and both are cheap.
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
	return dedupeRenderings(string(spaced), string(stripped))
}

// dedupeRenderings keeps each distinct reading once, in order, and adds the
// invisible-character-free reading of each. A blob can be binary AND carry a
// zero-width space inside a credential, and closing one of those without the
// other leaves the shortest path open.
func dedupeRenderings(candidates ...string) []string {
	renderings := make([]string, 0, len(candidates)*2)
	seen := make(map[string]bool, len(candidates)*2)
	add := func(text string) {
		if seen[text] {
			return
		}
		seen[text] = true
		renderings = append(renderings, text)
	}
	for _, candidate := range candidates {
		add(candidate)
		if HasInvisibleRunes(candidate) {
			add(withoutInvisibleRunes(candidate))
		}
	}
	return renderings
}

// Scan checks text for secret shapes and private identity markers.
func Scan(files []File, opts Options) Result {
	var result Result
	blocklist := append(DefaultBlocklist(), opts.Blocklist...)
	for _, file := range files {
		renderings := dedupeRenderings(file.Content)
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
