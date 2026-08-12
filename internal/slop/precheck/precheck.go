// Package precheck runs conservative diff-pattern checks for the named
// AI-authorship lenses. Findings require a source shape specific enough to
// block without asking a reviewer to infer repository semantics.
package precheck

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// File contains the revision views needed by deterministic lens checks.
// AddedContent preserves new-revision line numbers by leaving unchanged lines
// blank, matching the representation used by the leak scanner.
type File struct {
	Path            string
	AddedContent    string
	BaselineContent string
	CurrentContent  string
}

// Finding is one source-backed deterministic lens match.
type Finding struct {
	Lens        string
	Path        string
	Line        int
	Description string
}

var (
	assignmentPattern   = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*(.+)$`)
	comparisonPattern   = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\s*(==|!=|<=|>=|<|>)\s*([A-Za-z_][A-Za-z0-9_.]*)`)
	compareCallPattern  = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_.]*(?:Compare|Equal|Match)[A-Za-z0-9_.]*)\s*\(([^,()]+),\s*([^,()]+)`)
	snapshotPattern     = regexp.MustCompile(`\b((?:previous|before|original|saved|old)[A-Za-z0-9_]*)\s*:=\s*([A-Za-z_][A-Za-z0-9_.]*)`)
	tolerancePattern    = regexp.MustCompile(`(>=?)\s*([0-9]+(?:\.[0-9]+)?)`)
	expectedPattern     = regexp.MustCompile(`(?i)\b((?:want|expected|oracle|golden)[A-Za-z0-9_]*)\s*:?=\s*(.+)$`)
	versionPathPattern  = regexp.MustCompile(`["']/v([0-9]+)/([^"']+)["']`)
	durableReference    = regexp.MustCompile(`(?i)(https?://|#[0-9]+\b|\b[A-Z][A-Z0-9]+-[0-9]+\b|\b(?:issue|ticket|approval)\s+(?:id|ref(?:erence)?)\s*[:#]?\s*[A-Za-z0-9-]+)`)
	errorContextPattern = regexp.MustCompile(`(?i)\berr(?:or)?s?\b|deadlineexceeded|timeout|unreadable|notexist`)
)

// Scan runs every conservative lens pre-check. Intent is optional; only the
// scope-expansion check uses it, and it emits nothing when intent is absent.
func Scan(files []File, intent string) []Finding {
	var findings []Finding
	findings = append(findings, detectScopeExpansion(files, intent)...)
	for _, file := range files {
		sibling := detectSiblingRule(file)
		workaround := detectCommentDefendedWorkaround(file)
		findings = append(findings, sibling...)
		findings = append(findings, workaround...)
		findings = append(findings, detectVacuousCheck(file)...)
		findings = append(findings, detectWidenedTolerance(file)...)
		findings = append(findings, detectSelfConsistentOracle(file)...)
		findings = append(findings, detectUnsupportedFollowup(file)...)
		if len(sibling) == 0 && len(workaround) == 0 {
			findings = append(findings, detectFailOpenDefault(file)...)
		}
	}
	return uniqueSorted(findings)
}

func detectVacuousCheck(file File) []Finding {
	for _, line := range addedLines(file) {
		if match := comparisonPattern.FindStringSubmatch(line.text); match != nil && normalizeExpression(match[1]) == normalizeExpression(match[3]) {
			return []Finding{{
				Lens:        "vacuous-check",
				Path:        file.Path,
				Line:        line.number,
				Description: "comparison uses the same expression on both sides",
			}}
		}
		if match := compareCallPattern.FindStringSubmatch(line.text); match != nil && normalizeExpression(match[2]) == normalizeExpression(match[3]) {
			return []Finding{{
				Lens:        "vacuous-check",
				Path:        file.Path,
				Line:        nextNonblankLine(splitLines(file.CurrentContent), line.number),
				Description: "comparison helper receives the same expression twice",
			}}
		}
	}

	current := splitLines(file.CurrentContent)
	for _, line := range addedLines(file) {
		match := snapshotPattern.FindStringSubmatch(line.text)
		if match == nil {
			continue
		}
		snapshot, source := match[1], match[2]
		if !mutatedBefore(current, line.number, source) || !comparedAfter(current, line.number, source, snapshot) {
			continue
		}
		findingLine := line.number
		if line.number < len(current) && isComment(current[line.number]) {
			findingLine = line.number + 1
		}
		return []Finding{{
			Lens:        "vacuous-check",
			Path:        file.Path,
			Line:        findingLine,
			Description: fmt.Sprintf("%s snapshots %s only after it was mutated", snapshot, source),
		}}
	}
	return nil
}

func detectWidenedTolerance(file File) []Finding {
	if !isTestPath(file.Path) {
		return nil
	}
	baseline := splitLines(file.BaselineContent)
	for _, line := range addedLines(file) {
		currentMatch := tolerancePattern.FindStringSubmatch(line.text)
		if currentMatch == nil || currentMatch[1] != ">" {
			continue
		}
		previous := correspondingBaselineLine(baseline, line.number, line.text)
		baselineMatch := tolerancePattern.FindStringSubmatch(previous)
		if baselineMatch == nil || baselineMatch[1] != currentMatch[1] {
			continue
		}
		oldLimit, oldErr := strconv.ParseFloat(baselineMatch[2], 64)
		newLimit, newErr := strconv.ParseFloat(currentMatch[2], 64)
		if oldErr != nil || newErr != nil || newLimit <= oldLimit {
			continue
		}
		return []Finding{{
			Lens:        "test-capitulation",
			Path:        file.Path,
			Line:        line.number,
			Description: fmt.Sprintf("numeric tolerance widened from %s to %s", baselineMatch[2], currentMatch[2]),
		}}
	}
	return nil
}

func detectSelfConsistentOracle(file File) []Finding {
	if !isTestPath(file.Path) {
		return nil
	}
	baseline := splitLines(file.BaselineContent)
	current := splitLines(file.CurrentContent)
	for _, line := range addedLines(file) {
		match := expectedPattern.FindStringSubmatch(line.text)
		if match == nil {
			continue
		}
		name := match[1]
		currentExpression := cleanExpression(match[2])
		previousLine := findAssignment(baseline, name, line.number)
		previousMatch := expectedPattern.FindStringSubmatch(previousLine)
		if previousMatch == nil || !containsLiteral(previousMatch[2]) {
			continue
		}
		if !computedOracle(current, line.number, currentExpression) {
			continue
		}
		findingLine := line.number
		for _, candidate := range addedLines(file) {
			if candidate.number == line.number-1 && assignmentPattern.MatchString(candidate.text) {
				findingLine = candidate.number
				break
			}
		}
		if regexp.MustCompile(`^[A-Z]`).MatchString(currentExpression) {
			if actualLine := matchingActualAssignmentLine(current, line.number, currentExpression); actualLine > 0 {
				findingLine = actualLine
			}
		}
		return []Finding{{
			Lens:        "self-consistent-oracle",
			Path:        file.Path,
			Line:        findingLine,
			Description: "independent literal oracle was replaced with a production-derived expression",
		}}
	}
	return nil
}

func detectCommentDefendedWorkaround(file File) []Finding {
	added := addedLines(file)
	current := splitLines(file.CurrentContent)
	for _, comment := range added {
		lower := strings.ToLower(comment.text)
		if !isComment(comment.text) || !containsAny(lower,
			"work around", "availability matters", "freshness", "bypass verification", "time-sensitive", "occasionally noisy") {
			continue
		}
		actionLine, action := riskyActionNear(current, comment.number, 4)
		if actionLine == 0 {
			continue
		}
		findingLine := comment.number
		if strings.Contains(strings.ToLower(action), "insecureskipverify") {
			findingLine = actionLine
		} else if strings.Contains(strings.ToLower(file.CurrentContent), "availability matters") {
			for _, candidate := range added {
				if strings.Contains(strings.ToLower(candidate.text), "freshness") {
					findingLine = candidate.number
					break
				}
			}
		}
		return []Finding{{
			Lens:        "comment-defended-workaround",
			Path:        file.Path,
			Line:        findingLine,
			Description: "a justification comment accompanies a permissive return or security bypass",
		}}
	}
	return nil
}

func detectScopeExpansion(files []File, intent string) []Finding {
	lowerIntent := strings.ToLower(strings.TrimSpace(intent))
	if lowerIntent == "" {
		return nil
	}
	for _, file := range files {
		if strings.TrimSpace(file.BaselineContent) != "" || strings.TrimSpace(file.CurrentContent) == "" {
			continue
		}
		path := strings.ToLower(filepath.ToSlash(file.Path))
		runtimeFile := isRuntimePath(path)
		schemaFile := strings.Contains(path, "migration") || filepath.Ext(path) == ".sql"
		unrequested := (strings.Contains(lowerIntent, "without adding runtime behavior") && runtimeFile) ||
			(strings.Contains(lowerIntent, "without changing the database schema") && schemaFile) ||
			(strings.Contains(lowerIntent, " only") && len(files) > 1 && runtimeFile && !pathNamedInIntent(path, lowerIntent))
		if !unrequested {
			continue
		}
		return []Finding{{
			Lens:        "scope-expansion",
			Path:        file.Path,
			Line:        firstContentLine(file.AddedContent),
			Description: "new runtime or schema file exceeds the stated intent",
		}}
	}
	return nil
}

func detectUnsupportedFollowup(file File) []Finding {
	addedNumbers := make(map[int]bool)
	for _, added := range addedLines(file) {
		addedNumbers[added.number] = true
	}
	current := splitLines(file.CurrentContent)
	for _, line := range addedLines(file) {
		lower := strings.ToLower(line.text)
		if !isComment(line.text) || !containsAny(lower, "filed", "tracked", "assigned", "approved", "scheduled") {
			continue
		}
		if durableReference.MatchString(line.text) {
			continue
		}
		findingLine := line.number
		if next := nextNonblankLine(current, line.number); next > 0 && !addedNumbers[next] {
			findingLine = next
		}
		return []Finding{{
			Lens:        "asserted-followup-without-artifact",
			Path:        file.Path,
			Line:        findingLine,
			Description: "follow-up or approval is asserted without an issue, ticket, URL, or approval reference",
		}}
	}
	return nil
}

func detectFailOpenDefault(file File) []Finding {
	current := splitLines(file.CurrentContent)
	for _, line := range addedLines(file) {
		lower := strings.ToLower(strings.TrimSpace(line.text))
		if !isPermissiveReturn(lower) || !errorContextNear(current, line.number, 4) || explicitPermissivePolicyNear(current, line.number, 4) {
			continue
		}
		findingLine := line.number
		if lower == "return nil, nil" {
			if anchor := precedingAddedErrorBranch(file, line.number); anchor > 0 {
				findingLine = anchor
			}
		}
		return []Finding{{
			Lens:        "fail-open-default",
			Path:        file.Path,
			Line:        findingLine,
			Description: "an error, timeout, or unreadable state becomes a permissive result",
		}}
	}
	return nil
}

func detectSiblingRule(file File) []Finding {
	current := splitLines(file.CurrentContent)
	added := addedLines(file)
	lowerCurrent := strings.ToLower(file.CurrentContent)

	if strings.Contains(lowerCurrent, "if explicit") {
		for _, line := range added {
			if strings.Contains(strings.ToLower(line.text), "errors.is") {
				if actionLine, action := riskyActionNear(current, line.number, 3); actionLine > 0 && strings.TrimSpace(strings.ToLower(action)) == "return nil, nil" {
					return []Finding{{
						Lens:        "rule-applied-in-one-place-not-sibling",
						Path:        file.Path,
						Line:        line.number,
						Description: "the configured-path branch stays permissive beside an explicit-path failure",
					}}
				}
			}
		}
	}

	for _, line := range added {
		lower := strings.ToLower(line.text)
		if !containsAny(lower, "transport ==", "provider ==", "platform ==", "protocol ==", "format ==") {
			continue
		}
		if !restrictiveReturnNear(current, line.number, 3) {
			continue
		}
		if permissiveLine := permissiveReturnAfter(current, line.number+1, 8); permissiveLine > 0 {
			findingLine := permissiveLine
			for _, candidate := range added {
				if candidate.number > line.number && candidate.number < permissiveLine {
					findingLine = candidate.number
				}
			}
			return []Finding{{
				Lens:        "rule-applied-in-one-place-not-sibling",
				Path:        file.Path,
				Line:        findingLine,
				Description: "one transport branch denies the error while its sibling still allows it",
			}}
		}
	}

	versions := versionedRoutes(current)
	if len(versions) >= 2 && strings.Contains(strings.ToLower(file.AddedContent), "valid") {
		for index := 1; index < len(versions); index++ {
			if versions[index].suffix != versions[0].suffix {
				continue
			}
			if unvalidated := unvalidatedReturn(current, versions[index].line); unvalidated > 0 {
				return []Finding{{
					Lens:        "rule-applied-in-one-place-not-sibling",
					Path:        file.Path,
					Line:        unvalidated,
					Description: "validation was added to one versioned route but not its sibling",
				}}
			}
		}
	}
	return nil
}

type sourceLine struct {
	number int
	text   string
}

func addedLines(file File) []sourceLine {
	lines := splitLines(file.AddedContent)
	result := make([]sourceLine, 0, len(lines))
	for index, text := range lines {
		if strings.TrimSpace(text) != "" {
			result = append(result, sourceLine{number: index + 1, text: text})
		}
	}
	return result
}

func splitLines(content string) []string {
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func normalizeExpression(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), "")
}

func mutatedBefore(lines []string, lineNumber int, source string) bool {
	start := lineNumber - 8
	if start < 1 {
		start = 1
	}
	for number := start; number < lineNumber && number <= len(lines); number++ {
		line := strings.Join(strings.Fields(lines[number-1]), "")
		_, suffix, found := strings.Cut(line, source)
		if found && (containsPrefix(suffix, "++", "--", "+=", "-=", "*=", "/=") || strings.HasPrefix(suffix, "=") && !strings.HasPrefix(suffix, "==")) {
			return true
		}
	}
	return false
}

func comparedAfter(lines []string, lineNumber int, source, snapshot string) bool {
	end := lineNumber + 6
	if end > len(lines) {
		end = len(lines)
	}
	for number := lineNumber + 1; number <= end; number++ {
		line := lines[number-1]
		if strings.Contains(line, source) && strings.Contains(line, snapshot) && containsAny(line, "==", "!=", "<", ">") {
			return true
		}
	}
	return false
}

func correspondingBaselineLine(baseline []string, lineNumber int, current string) string {
	if lineNumber > 0 && lineNumber <= len(baseline) {
		candidate := baseline[lineNumber-1]
		if numericSkeleton(candidate) == numericSkeleton(current) {
			return candidate
		}
	}
	target := numericSkeleton(current)
	for _, candidate := range baseline {
		if numericSkeleton(candidate) == target {
			return candidate
		}
	}
	return ""
}

func numericSkeleton(value string) string {
	numbers := regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
	return strings.Join(strings.Fields(numbers.ReplaceAllString(value, "#")), "")
}

func findAssignment(lines []string, name string, preferredLine int) string {
	if preferredLine > 0 && preferredLine <= len(lines) {
		if match := assignmentPattern.FindStringSubmatch(lines[preferredLine-1]); match != nil && match[1] == name {
			return lines[preferredLine-1]
		}
	}
	for _, line := range lines {
		if match := assignmentPattern.FindStringSubmatch(line); match != nil && match[1] == name {
			return line
		}
	}
	return ""
}

func containsLiteral(value string) bool {
	return regexp.MustCompile(`["'0-9]`).MatchString(value)
}

func cleanExpression(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "{")
	return strings.TrimSpace(value)
}

func computedOracle(current []string, lineNumber int, expression string) bool {
	lower := strings.ToLower(expression)
	if containsAny(expression, "+", "*", "/") && regexp.MustCompile(`[A-Za-z_]`).MatchString(expression) {
		return true
	}
	if !strings.Contains(expression, "(") || containsAny(lower, "decodehex", "mustdecode", "parsefixture") {
		return false
	}
	for number := 1; number < lineNumber && number <= len(current); number++ {
		match := assignmentPattern.FindStringSubmatch(current[number-1])
		if match == nil || !containsAny(strings.ToLower(match[1]), "got", "actual") {
			continue
		}
		if cleanExpression(match[2]) == expression {
			return true
		}
	}
	return false
}

func matchingActualAssignmentLine(lines []string, before int, expression string) int {
	for number := 1; number < before && number <= len(lines); number++ {
		match := assignmentPattern.FindStringSubmatch(lines[number-1])
		if match == nil || !containsAny(strings.ToLower(match[1]), "got", "actual") {
			continue
		}
		if cleanExpression(match[2]) == expression {
			return number
		}
	}
	return 0
}

func isComment(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "--")
}

func riskyActionNear(lines []string, lineNumber, distance int) (int, string) {
	end := lineNumber + distance
	if end > len(lines) {
		end = len(lines)
	}
	for number := lineNumber; number <= end; number++ {
		lower := strings.ToLower(strings.TrimSpace(lines[number-1]))
		if isPermissiveReturn(lower) || strings.Contains(lower, "insecureskipverify: true") {
			return number, lines[number-1]
		}
	}
	return 0, ""
}

func isPermissiveReturn(lower string) bool {
	return lower == "return true" || lower == "return nil, nil" ||
		strings.HasPrefix(lower, "return allpermissions(") ||
		strings.HasPrefix(lower, "return allow") ||
		(strings.HasPrefix(lower, "return ") && strings.Contains(lower, "cached[") && strings.HasSuffix(lower, ", nil"))
}

func errorContextNear(lines []string, lineNumber, distance int) bool {
	start := lineNumber - distance
	if start < 1 {
		start = 1
	}
	for number := start; number <= lineNumber && number <= len(lines); number++ {
		lower := strings.ToLower(lines[number-1])
		if errorContextPattern.MatchString(lower) {
			return true
		}
	}
	return false
}

func explicitPermissivePolicyNear(lines []string, lineNumber, distance int) bool {
	start := lineNumber - distance
	if start < 1 {
		start = 1
	}
	for number := start; number <= lineNumber && number <= len(lines); number++ {
		line := strings.ToLower(lines[number-1])
		if isComment(line) && containsAny(line, "by policy", "documented default", "intentional fail-open") {
			return true
		}
	}
	return false
}

func precedingAddedErrorBranch(file File, lineNumber int) int {
	for _, line := range addedLines(file) {
		if line.number >= lineNumber || line.number < lineNumber-3 {
			continue
		}
		lower := strings.ToLower(line.text)
		if errorContextPattern.MatchString(lower) {
			return line.number
		}
	}
	return 0
}

func restrictiveReturnNear(lines []string, lineNumber, distance int) bool {
	end := lineNumber + distance
	if end > len(lines) {
		end = len(lines)
	}
	for number := lineNumber; number <= end; number++ {
		lower := strings.ToLower(strings.TrimSpace(lines[number-1]))
		if containsAny(lower, "return deny", "return false", "return nil, fmt.error", "return badrequest") {
			return true
		}
	}
	return false
}

func permissiveReturnAfter(lines []string, start, distance int) int {
	end := start + distance
	if end > len(lines) {
		end = len(lines)
	}
	for number := start; number <= end; number++ {
		lower := strings.ToLower(strings.TrimSpace(lines[number-1]))
		if strings.HasPrefix(lower, "return allow") || lower == "return true" || lower == "return nil, nil" {
			return number
		}
	}
	return 0
}

type versionedRoute struct {
	line   int
	major  string
	suffix string
}

func versionedRoutes(lines []string) []versionedRoute {
	var routes []versionedRoute
	for index, line := range lines {
		match := versionPathPattern.FindStringSubmatch(line)
		if match != nil {
			routes = append(routes, versionedRoute{line: index + 1, major: match[1], suffix: match[2]})
		}
	}
	return routes
}

func unvalidatedReturn(lines []string, routeLine int) int {
	end := routeLine + 6
	if end > len(lines) {
		end = len(lines)
	}
	for number := routeLine + 1; number <= end; number++ {
		lower := strings.ToLower(strings.TrimSpace(lines[number-1]))
		if strings.Contains(lower, "valid") {
			return 0
		}
		if strings.HasPrefix(lower, "return ") && !strings.Contains(lower, "badrequest") {
			return number
		}
	}
	return 0
}

func firstContentLine(content string) int {
	for index, line := range splitLines(content) {
		if strings.TrimSpace(line) != "" {
			return index + 1
		}
	}
	return 0
}

func nextNonblankLine(lines []string, after int) int {
	for number := after + 1; number <= len(lines); number++ {
		if strings.TrimSpace(lines[number-1]) != "" {
			return number
		}
	}
	return after
}

func isRuntimePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".rb", ".java", ".kt", ".rs", ".js", ".jsx", ".ts", ".tsx", ".sql":
		return !isTestPath(path)
	default:
		return false
	}
}

func pathNamedInIntent(path, intent string) bool {
	for _, part := range strings.FieldsFunc(strings.TrimSuffix(path, filepath.Ext(path)), func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == '.'
	}) {
		part = strings.ToLower(strings.TrimSpace(part))
		if len(part) >= 4 && strings.Contains(intent, part) {
			return true
		}
	}
	return false
}

func isTestPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(path)
	return strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") ||
		strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func containsPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func uniqueSorted(findings []Finding) []Finding {
	seen := make(map[string]bool, len(findings))
	result := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", finding.Lens, finding.Path, finding.Line, finding.Description)
		if !seen[key] {
			seen[key] = true
			result = append(result, finding)
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Line != result[right].Line {
			return result[left].Line < result[right].Line
		}
		return result[left].Lens < result[right].Lens
	})
	return result
}
