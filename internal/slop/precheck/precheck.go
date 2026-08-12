// Package precheck runs conservative diff-pattern checks for the named
// AI-authorship lenses. Findings require a source shape specific enough to
// block without asking a reviewer to infer repository semantics.
package precheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// File contains the revision views needed by deterministic lens checks.
// AddedContent preserves new-revision line numbers by leaving unchanged lines
// blank, matching the representation used by the leak scanner.
type File struct {
	Path                   string
	BaselinePath           string
	CommentBaselinePath    string
	AddedContent           string
	CommentAddedContent    string
	BaselineContent        string
	CommentBaselineContent string
	CurrentContent         string
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
	declarationPattern  = regexp.MustCompile(`^(?:func\s+(?:\([^)]*\)\s*)?|type\s+|(?:var|const)\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	declarationBlock    = regexp.MustCompile(`^(?:type|var|const)\s*\($`)
)

type lexedSourceLine struct {
	comments []lexedComment
	code     string
}

type lexedComment struct {
	standalone bool
	commentID  int
	body       string
}

// docFillerWords carry no information beyond the declaration they document, so
// a doc comment built only from them plus the declaration's own words restates
// the name rather than explaining it.
var docFillerWords = map[string]bool{
	"are": true, "behavior": true, "behaviour": true, "case": true, "cases": true,
	"check": true, "checks": true, "do": true, "does": true, "ensure": true,
	"ensures": true, "for": true, "function": true, "given": true, "handle": true,
	"handles": true, "helper": true, "here": true, "if": true, "in": true,
	"is": true, "it": true, "its": true, "method": true, "new": true,
	"of": true, "report": true, "reports": true, "test": true, "tests": true,
	"that": true, "used": true, "using": true, "value": true, "values": true,
	"verifies": true, "when": true, "whether": true, "with": true,
}

var qualificationWords = map[string]bool{
	"after": true, "before": true, "despite": true, "during": true,
	"except": true, "never": true, "only": true, "once": true,
	"unless": true, "until": true, "when": true, "whether": true,
	"while": true, "without": true,
}

// Scan runs every conservative lens pre-check. Intent is optional; only the
// scope-expansion check uses it, and it emits nothing when intent is absent.
func Scan(files []File, intent string) []Finding {
	var findings []Finding
	findings = append(findings, detectScopeExpansion(files, intent)...)
	for _, file := range files {
		findings = append(findings, detectRedundantComment(file)...)
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

func detectRedundantComment(file File) []Finding {
	var findings []Finding
	current := splitLines(file.CurrentContent)
	lexed, declarations := lexSource(file.Path, current)
	baselinePath := file.CommentBaselinePath
	baselineContent := file.CommentBaselineContent
	addedContent := file.CommentAddedContent
	if baselinePath == "" {
		baselinePath = file.BaselinePath
		if baselinePath == "" {
			baselinePath = file.Path
		}
		baselineContent = file.BaselineContent
		addedContent = file.AddedContent
	}
	baseline, _ := lexSource(baselinePath, splitLines(baselineContent))
	for _, block := range commentBlocks(addedContent, lexed, baseline) {
		description := ""
		switch {
		case hasRepeatedPhrase(block.text, declarations[block.lastLine]):
			description = "comment repeats a phrase internally"
		case docCommentRepeatsDeclarationName(block.text, declarations[block.lastLine]):
			description = "doc comment adds no information beyond the declaration name"
		case block.standalone && commentRestatesNextCode(block.text, current, lexed, block.lastLine):
			description = "comment restates the adjacent code"
		}
		if description != "" {
			findings = append(findings, Finding{
				Lens:        "redundant-comment",
				Path:        file.Path,
				Line:        block.line,
				Description: description,
			})
		}
	}
	return findings
}

type commentBlock struct {
	line       int
	lastLine   int
	text       string
	standalone bool
}

// commentBlocks groups contiguous comment lines into the single comment a
// reader sees. Judging each line on its own misreads a wrapped doc comment: its
// first line often holds nothing but the declaration name, and its later lines
// are sentence fragments that never document the code beneath the block. Only
// blocks the change actually touched are returned, reported at their first
// added line.
func commentBlocks(addedContent string, lines, baseline []lexedSourceLine) []commentBlock {
	added := make(map[int]bool)
	for _, line := range addedContentLines(addedContent) {
		added[line.number] = true
	}
	baselineComments := make(map[string]int)
	for _, block := range allCommentBlocks(baseline) {
		if block.text != "" {
			baselineComments[block.text]++
		}
	}
	current := allCommentBlocks(lines)
	for _, block := range current {
		if firstAddedLine(block, added) != 0 || baselineComments[block.text] == 0 {
			continue
		}
		baselineComments[block.text]--
	}
	var blocks []commentBlock
	for _, block := range current {
		line := firstAddedLine(block, added)
		if line == 0 {
			continue
		}
		existed := false
		if block.text != "" && baselineComments[block.text] > 0 {
			baselineComments[block.text]--
			existed = true
		}
		block.line = line
		if block.text != "" && !existed {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func addedContentLines(content string) []sourceLine {
	return addedLines(File{AddedContent: content})
}

func firstAddedLine(block commentBlock, added map[int]bool) int {
	for number := block.line; number <= block.lastLine; number++ {
		if added[number] {
			return number
		}
	}
	return 0
}

func allCommentBlocks(lines []lexedSourceLine) []commentBlock {
	type fragment struct {
		line int
		lexedComment
	}
	var fragments []fragment
	for index, line := range lines {
		for _, comment := range line.comments {
			fragments = append(fragments, fragment{line: index + 1, lexedComment: comment})
		}
	}
	var blocks []commentBlock
	for index := 0; index < len(fragments); index++ {
		current := fragments[index]
		block := commentBlock{line: current.line, lastLine: current.line, standalone: current.standalone}
		for {
			if !isCodeSample(current.body) {
				if block.text != "" {
					block.text += "\n"
				}
				block.text += strings.TrimSpace(current.body)
			}
			if index+1 >= len(fragments) {
				break
			}
			next := fragments[index+1]
			if next.standalone != block.standalone || block.standalone && next.line != current.line+1 || !block.standalone && next.commentID != current.commentID {
				break
			}
			index++
			current = next
			block.lastLine = current.line
		}
		block.text = strings.TrimSpace(block.text)
		if block.text != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// docCommentRepeatsDeclarationName reports a doc comment whose every
// informative word is already spelled by the declaration it documents. Go's
// convention is that a doc comment opens with the declaration name, so merely
// naming the declaration is not the signal; adding nothing else is.
func docCommentRepeatsDeclarationName(comment, declarationName string) bool {
	if declarationName == "" {
		return false
	}
	nameWords := identifierWords(declarationName)
	derived := 0
	informative := 0
	for _, word := range meaningfulWords(comment) {
		if docFillerWords[word] {
			continue
		}
		informative++
		if nameWords[word] {
			derived++
		}
	}
	return derived > 0 && derived == informative
}

func commentRestatesNextCode(comment string, lines []string, lexed []lexedSourceLine, after int) bool {
	code := adjacentCodeLine(lines, lexed, after)
	if code == "" || isDeclaration(code) {
		return false
	}
	commentWords := meaningfulWords(comment)
	if len(commentWords) < 2 {
		return false
	}
	codeWords := make(map[string]bool)
	for _, word := range wordTokens(code) {
		codeWords[word] = true
	}
	compact := strings.Join(strings.Fields(code), "")
	if strings.Contains(compact, "++") || strings.Contains(compact, "+=") {
		codeWords["increment"] = true
	}
	if strings.Contains(compact, "--") || strings.Contains(compact, "-=") {
		codeWords["decrement"] = true
	}
	for _, word := range commentWords {
		if qualificationWords[word] && !codeWords[word] {
			return false
		}
	}
	overlap := 0
	for _, word := range commentWords {
		if codeWords[word] {
			overlap++
		}
	}
	return float64(overlap)/float64(len(commentWords)) >= 0.75
}

// adjacentCodeLine returns the code a comment documents: the next non-comment
// line reached without crossing a blank line. A blank line ends the comment's
// attachment, so a standalone note is never read as documenting whatever
// declaration happens to appear further down the file.
func adjacentCodeLine(lines []string, lexed []lexedSourceLine, after int) string {
	for number := after + 1; number <= len(lines); number++ {
		candidate := strings.TrimSpace(lines[number-1])
		if candidate == "" {
			return ""
		}
		if len(lexed[number-1].comments) == 0 {
			return candidate
		}
		if lexed[number-1].code != "" {
			return lexed[number-1].code
		}
	}
	return ""
}

func isDeclaration(value string) bool {
	trimmed := strings.TrimSpace(value)
	return declarationPattern.MatchString(trimmed) || declarationBlock.MatchString(trimmed)
}

// identifierWords splits a declaration name into the words a doc comment could
// legitimately reuse: the whole lowercased name plus its camelCase, snake_case,
// and acronym parts, so "TestRejectsEmptyInput" also spells "empty" and "input".
func identifierWords(name string) map[string]bool {
	words := map[string]bool{strings.ToLower(name): true}
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words[strings.ToLower(string(current))] = true
			current = nil
		}
	}
	runes := []rune(name)
	for index, letter := range runes {
		switch {
		case letter == '_':
			flush()
			continue
		case unicode.IsUpper(letter) && index > 0:
			previous := runes[index-1]
			next := rune(0)
			if index+1 < len(runes) {
				next = runes[index+1]
			}
			if !unicode.IsUpper(previous) || unicode.IsLower(next) {
				flush()
			}
		}
		current = append(current, letter)
	}
	flush()
	return words
}

func meaningfulWords(value string) []string {
	stop := map[string]bool{
		"a": true, "an": true, "and": true, "as": true, "the": true,
		"this": true, "to": true, "we": true,
	}
	var result []string
	for _, word := range wordTokens(value) {
		if !stop[word] {
			result = append(result, word)
		}
	}
	return result
}

func lexSource(path string, lines []string) ([]lexedSourceLine, map[int]string) {
	result := make([]lexedSourceLine, len(lines))
	declarations := make(map[int]string)
	if !strings.EqualFold(filepath.Ext(path), ".go") {
		return result, declarations
	}
	content := strings.Join(lines, "\n")
	files := token.NewFileSet()
	file := files.AddFile(path, -1, len(content))
	var lexer scanner.Scanner
	lexer.Init(file, []byte(content), nil, scanner.ScanComments)
	type commentRange struct {
		start int
		end   int
	}
	ranges := make([][]commentRange, len(lines))
	commentID := 0
	for {
		position, kind, literal := lexer.Scan()
		if kind == token.EOF {
			break
		}
		if kind != token.COMMENT {
			continue
		}
		commentID++
		start := files.PositionFor(position, false)
		if start.Line < 1 || start.Line > len(lines) || start.Column < 1 || start.Column > len(lines[start.Line-1])+1 {
			continue
		}
		parts := strings.Split(literal, "\n")
		for offset, part := range parts {
			line := start.Line + offset
			if line > len(result) {
				break
			}
			if strings.HasPrefix(part, "//") {
				part = strings.TrimPrefix(part, "//")
			} else {
				if offset == 0 {
					part = strings.TrimPrefix(part, "/*")
				}
				if offset == len(parts)-1 {
					part = strings.TrimSuffix(part, "*/")
				}
			}
			rangeStart := 0
			if offset == 0 {
				rangeStart = start.Column - 1
			}
			rangeEnd := len(lines[line-1])
			if offset == len(parts)-1 {
				rangeEnd = rangeStart + len(parts[offset])
			}
			ranges[line-1] = append(ranges[line-1], commentRange{start: rangeStart, end: rangeEnd})
			result[line-1].comments = append(result[line-1].comments, lexedComment{commentID: commentID, body: blockBody(part)})
		}
	}
	for index, source := range lines {
		var code strings.Builder
		cursor := 0
		for _, span := range ranges[index] {
			if span.start > cursor {
				code.WriteString(source[cursor:span.start])
			}
			if span.end > cursor {
				cursor = span.end
			}
		}
		code.WriteString(source[cursor:])
		result[index].code = strings.TrimSpace(code.String())
	}
	inline := make(map[int]bool)
	for _, line := range result {
		if line.code == "" {
			continue
		}
		for _, comment := range line.comments {
			inline[comment.commentID] = true
		}
	}
	for line := range result {
		for index := range result[line].comments {
			result[line].comments[index].standalone = !inline[result[line].comments[index].commentID]
		}
	}
	declarationFiles := token.NewFileSet()
	parsed, err := parser.ParseFile(declarationFiles, path, content, parser.ParseComments)
	lineOffset := 0
	if err != nil {
		declarationFiles = token.NewFileSet()
		parsed, err = parser.ParseFile(declarationFiles, path, "package precheck\n"+content, parser.ParseComments)
		lineOffset = 1
	}
	if err == nil {
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.FuncDecl:
				addDeclaration(declarations, declarationFiles, declaration.Doc, declaration.Name.Name, lineOffset)
			case *ast.GenDecl:
				if len(declaration.Specs) == 1 {
					addDeclaration(declarations, declarationFiles, declaration.Doc, specName(declaration.Specs[0]), lineOffset)
				}
			case *ast.TypeSpec:
				addDeclaration(declarations, declarationFiles, declaration.Doc, declaration.Name.Name, lineOffset)
			case *ast.ValueSpec:
				if len(declaration.Names) == 1 {
					addDeclaration(declarations, declarationFiles, declaration.Doc, declaration.Names[0].Name, lineOffset)
				}
			}
			return true
		})
	}
	return result, declarations
}

func addDeclaration(declarations map[int]string, files *token.FileSet, comment *ast.CommentGroup, name string, lineOffset int) {
	if comment == nil || name == "" {
		return
	}
	line := files.PositionFor(comment.End(), false).Line - lineOffset
	declarations[line] = name
}

func specName(spec ast.Spec) string {
	switch declaration := spec.(type) {
	case *ast.TypeSpec:
		return declaration.Name.Name
	case *ast.ValueSpec:
		if len(declaration.Names) == 1 {
			return declaration.Names[0].Name
		}
	}
	return ""
}

func blockBody(value string) string {
	marker := strings.IndexFunc(value, func(letter rune) bool {
		return !unicode.IsSpace(letter)
	})
	if marker >= 0 && value[marker] == '*' {
		return value[marker+1:]
	}
	return value
}

// isCodeSample reports an indented line inside a comment, which is how Go doc
// comments and Markdown-style comments mark an embedded command or code block.
// Its repeated tokens are the sample's own syntax, not restated prose.
func isCodeSample(body string) bool {
	return strings.HasPrefix(body, "\t") || strings.HasPrefix(body, "    ")
}

// hasRepeatedPhrase looks for a substantive clause restated at the end of the
// next clause. Shorter windows and phrases embedded in different qualifications
// are not a source shape specific enough to block on.
func hasRepeatedPhrase(comment, declarationName string) bool {
	var clauses [][]string
	for _, clause := range strings.FieldsFunc(comment, func(letter rune) bool {
		return letter == ';' || letter == '.' || letter == '!' || letter == '?' || letter == '\n'
	}) {
		words := meaningfulWords(clause)
		if len(words) >= 4 {
			clauses = append(clauses, words)
		}
	}
	for index := 1; index < len(clauses); index++ {
		previous, current := clauses[index-1], clauses[index]
		declarationWords := identifierWords(declarationName)
		if equalWords(previous, current) || len(previous) == len(current)+1 && declarationWords[previous[0]] && equalWords(previous[1:], current) {
			return true
		}
	}
	return false
}

func equalWords(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func wordTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	})
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
		if strings.TrimSpace(file.CurrentContent) == "" {
			continue
		}
		path := strings.ToLower(filepath.ToSlash(file.Path))
		baselinePath := file.BaselinePath
		if baselinePath == "" {
			baselinePath = file.Path
		}
		baselinePath = strings.ToLower(filepath.ToSlash(baselinePath))
		runtimeFile := isRuntimePath(path)
		schemaFile := isSchemaPath(path)
		newRuntimeScope := runtimeFile && (strings.TrimSpace(file.BaselineContent) == "" || !isRuntimePath(baselinePath))
		newSchemaScope := schemaFile && (strings.TrimSpace(file.BaselineContent) == "" || !isSchemaPath(baselinePath))
		unrequested := (strings.Contains(lowerIntent, "without adding runtime behavior") && newRuntimeScope) ||
			(strings.Contains(lowerIntent, "without changing the database schema") && newSchemaScope) ||
			(strings.Contains(lowerIntent, " only") && len(files) > 1 && newRuntimeScope && !pathNamedInIntent(path, lowerIntent))
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
	path = strings.ToLower(filepath.ToSlash(path))
	if strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, "examples/") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".rb", ".java", ".kt", ".rs", ".js", ".jsx", ".ts", ".tsx", ".sql":
		return !isTestPath(path)
	default:
		return false
	}
}

func isSchemaPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	if strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, "examples/") || isTestPath(path) {
		return false
	}
	return strings.Contains(path, "migration") || filepath.Ext(path) == ".sql"
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
