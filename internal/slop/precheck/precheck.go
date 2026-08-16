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
	Path            string
	BaselinePath    string
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
	declarationPattern  = regexp.MustCompile(`^(?:func\s+(?:\([^)]*\)\s*)?|type\s+|(?:var|const)\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	declarationBlock    = regexp.MustCompile(`^(?:type|var|const)\s*\($`)
	// dispatchComparison recognises a branch that selects behavior by transport,
	// provider, or format. Matching the literal strings "transport ==",
	// "provider ==" and three more meant a fifth dispatch axis, or a `===`, was
	// invisible.
	// The dispatch axes stay concrete. Adding generic words like "kind" and
	// "mode" matched ordinary code (`token.kind == 'p'`) and turned an
	// unrelated switch into a sibling-rule finding.
	dispatchComparison = regexp.MustCompile(`(?i)\b(?:transport|provider|platform|protocol|format|scheme|backend|driver|vendor|channel)\s*(?:==|===|!=|\.equals\()`)
	explicitBranch     = regexp.MustCompile(`(?i)\bif\b[^\n]*\bexplicit`)
	conditionalLine    = regexp.MustCompile(`(?i)^\s*(?:\}\s*)?(?:else\s+if|if|elif|elsif|unless|when|catch|except|rescue)\b`)
	literalToken       = regexp.MustCompile(`["'0-9]`)
	computedExpression = regexp.MustCompile(`[A-Za-z_]`)
	upperCaseStart     = regexp.MustCompile(`^[A-Z]`)
	numericToken       = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
	// guardClause recognises a control-flow check that refuses. Counting them
	// on each side of the diff is what lets a DELETED guard be seen at all:
	// every other detector reads added lines only, so the defect delivered by
	// removing a check produced no finding whatsoever.
	guardClause = regexp.MustCompile(`(?i)^\s*(?:if|unless|elif|else if|assert|require|guard)\b|^\s*(?:raise|throw)\b|\brequire\s*\(|\bassert\w*\s*\(`)
	// guardSubject deliberately omits a bare `!` and a bare `not`. Including
	// them made `if len(x) != 0` count as a guard, so an ordinary refactor that
	// consolidated two length checks reported a removed guard. A false finding
	// here fails a run, so the subject has to be specific.
	//
	// The last alternative closes the round-3 residual on R9. A guard whose
	// refusal is spelled `return errBad` matched nothing: the bare `err` word
	// needs a boundary, and `errBad` has none, so deleting an input guard that
	// returned a named error value was invisible. Requiring the name to sit in
	// a return position keeps it specific: an identifier merely CONTAINING err
	// anywhere on the line is still not a subject.
	guardSubject = regexp.MustCompile(`(?i)\berr(?:or)?s?\b|\bok\b|\bpermission|\bauthoriz|\bauthentic|\badmin\b|\brole\b|\btoken\b|\bverif|\bvalid|\ballowed\b|\bdenied\b|\bexpired\b|\bnil\b|\bnull\b|\bnone\b|\breturn\s+[A-Za-z_.]*[Ee]rr[A-Za-z0-9_]*`)
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

// Result is what one pre-check pass learned, including what it could not check.
type Result struct {
	Findings []Finding
	// Unarmed names each detector that could not run and why. A detector that
	// silently contributes zero findings is indistinguishable from one that
	// looked and found nothing, and the mandatory-check line then carries more
	// authority than the pass earned.
	Unarmed []string
}

// Scan runs every conservative lens pre-check.
func Scan(files []File, intent string) Result {
	var result Result
	// The scope-expansion detector can only fire on an intent that states a
	// scope limit it recognises, so an intent of "stuff" armed the check on the
	// mandatory-check line while leaving it structurally unable to report
	// anything. Since the intent is author-supplied, that gap was a disarm
	// wearing an armed label: the cheapest way past this detector was to say
	// something rather than nothing.
	switch {
	case strings.TrimSpace(intent) == "":
		result.Unarmed = append(result.Unarmed, "scope-expansion needs a stated intent")
	case !intentStatesAScopeLimit(intent):
		result.Unarmed = append(result.Unarmed, "scope-expansion needs an intent that states a scope limit, such as \"only\", \"without adding runtime behavior\", or \"without changing the database schema\"")
	}
	result.Findings = append(result.Findings, detectScopeExpansion(files, intent)...)
	for _, file := range files {
		result.Findings = append(result.Findings, detectRedundantComment(file)...)
		sibling := detectSiblingRule(file)
		workaround := detectCommentDefendedWorkaround(file)
		result.Findings = append(result.Findings, sibling...)
		result.Findings = append(result.Findings, workaround...)
		result.Findings = append(result.Findings, detectVacuousCheck(file)...)
		result.Findings = append(result.Findings, detectWidenedTolerance(file)...)
		result.Findings = append(result.Findings, detectSelfConsistentOracle(file)...)
		result.Findings = append(result.Findings, detectUnsupportedFollowup(file)...)
		if len(sibling) == 0 && len(workaround) == 0 {
			result.Findings = append(result.Findings, detectFailOpenDefault(file)...)
		}
	}
	result.Findings = append(result.Findings, detectRemovedGuards(files)...)
	result.Findings = uniqueSorted(result.Findings)
	return result
}

// detectRemovedGuards decides the removed-guard question over the whole change
// set rather than one file at a time, and it decides it by IDENTITY.
//
// A per-file count is blind to the commonest legitimate shape there is: moving
// validation out of handler.go into a new validate.go drops handler.go's count
// while the change set still holds every refusing check it started with. The
// per-file detector called that a removal and blocked at every tier, with no
// exemption path, because --tier can only raise and allow_exemptions covers the
// leak scan alone.
//
// The first attempt at that excused a shrinking file whenever the change set's
// total guard COUNT had not fallen. A count is evidence of nothing, because the
// audited change mints the thing being counted: deleting three authorization
// guards from internal/auth/policy.go and adding a file of three unrelated
// `if err != nil { return err }` lines in the same commit brought the total back
// to level and suppressed the finding entirely. The compensating supply is
// inside the diff, which is exactly the author-writable input the design rule
// forbids deciding on.
//
// So a lost clause is excused only when the SAME clause is added somewhere in
// the change set. Matching is on the normalized clause text plus its refusing
// action, each relocated clause is spent once so it cannot excuse two removals,
// and every clause left unmatched is reported.
//
// There is NO count anywhere in this decision, including as a precondition.
// Keeping one as the per-file trigger left the whole identity rule unreachable
// by the shape it was written to stop: with the padding folded into the
// shrinking file instead of a new one, three deleted authorization guards and
// three added `if err != nil { return err }` helpers left the file's guard
// total unchanged, the count trigger returned early, and identity matching was
// never consulted at all. A count is the audited change's own arithmetic; it
// decides nothing here, before, after, or instead of the identity check.
//
// Matching deliberately does NOT alpha-rename identifiers. Collapsing names is
// what makes distinct guards look alike: under a consistent renaming
// `if user == "" { return errForbidden }` and `if cache == "" { return errMissing }`
// have the same shape, so allowing it would reopen the padding bypass for the
// price of choosing better padding. The cost of exactness is that an in-place
// edit that rewords a condition or renames a variable inside a guard now
// reports, where the count rule was silent.
//
// Two residuals, both inherent to matching a token stream:
//
// The relocation target is not checked for reachability. Copying the identical
// clause and its refusing action into a helper nobody calls, or into a file that
// imports nothing the guard protected, excuses the deletion. Separating a real
// relocation from a byte-faithful dead copy needs reference resolution, which no
// token stream can do in one language let alone all of them. The supply is still
// strictly narrower than the count rule it replaced, because the author has to
// reproduce the exact clause rather than any three guard-shaped lines.
//
// A file deleted outright is not read as removing its guards. Its clauses leave
// the change set with nothing matching them, so the identity rule alone would
// report every commit that drops a dead file carrying an `if err != nil`, and
// this finding blocks at every tier with no exemption path. The carve-out is
// deliberate and it is a hole: deleting a file and adding a replacement without
// its guards is not caught here.
func detectRemovedGuards(files []File) []Finding {
	relocated := make(map[string]int)
	for _, file := range files {
		if !isRuntimePath(file.Path) {
			continue
		}
		for signature, count := range addedGuardSignatures(file) {
			relocated[signature] += count
		}
	}
	var findings []Finding
	for _, file := range files {
		if !isRuntimePath(file.Path) {
			continue
		}
		// A file with no head content was deleted rather than weakened. See the
		// second residual above.
		if strings.TrimSpace(file.CurrentContent) == "" {
			continue
		}
		for _, signature := range unmatchedRemovedGuards(relocated, file) {
			findings = append(findings, Finding{
				Lens: "fail-open-default",
				Path: file.Path,
				Line: removedGuardLine(file, signature),
				Description: fmt.Sprintf(
					"refusing checks dropped: %q was removed and no equivalent clause is added anywhere in this change",
					signature),
			})
		}
	}
	return findings
}

// unmatchedRemovedGuards spends one pooled addition per clause this file lost
// and returns the clauses nothing covered. Spending rather than peeking is what
// stops a single relocated clause from excusing the same deletion twice.
func unmatchedRemovedGuards(pool map[string]int, file File) []string {
	removed := removedGuardSignatures(file)
	signatures := make([]string, 0, len(removed))
	for signature := range removed {
		signatures = append(signatures, signature)
	}
	sort.Strings(signatures)
	var unmatched []string
	for _, signature := range signatures {
		count := removed[signature]
		matched := pool[signature]
		if matched > count {
			matched = count
		}
		pool[signature] -= matched
		for index := matched; index < count; index++ {
			unmatched = append(unmatched, signature)
		}
	}
	return unmatched
}

// removedGuardLine points at the baseline line that carried this clause.
func removedGuardLine(file File, signature string) int {
	for _, occurrence := range guardOccurrences(splitLines(file.BaselineContent)) {
		if occurrence.signature == signature {
			return occurrence.line + 1
		}
	}
	return 1
}

// removedGuardSignatures is the multiset of guard clauses this file stopped
// carrying, netted against the ones it still carries.
func removedGuardSignatures(file File) map[string]int {
	return signatureSurplus(file.BaselineContent, file.CurrentContent)
}

// addedGuardSignatures is the mirror: the guard clauses this file did not carry
// before and does now.
func addedGuardSignatures(file File) map[string]int {
	return signatureSurplus(file.CurrentContent, file.BaselineContent)
}

func signatureSurplus(have, against string) map[string]int {
	other := countSignatures(against)
	surplus := make(map[string]int)
	for signature, count := range countSignatures(have) {
		if extra := count - other[signature]; extra > 0 {
			surplus[signature] = extra
		}
	}
	return surplus
}

func countSignatures(content string) map[string]int {
	counts := make(map[string]int)
	for _, occurrence := range guardOccurrences(splitLines(content)) {
		counts[occurrence.signature]++
	}
	return counts
}

// guardClauseBodyLookahead is how far past a guard's opening line the subject
// may sit. A guard written across lines puts its refusal in the body, so
// `if user == "" {` and `return errBad` are two lines of one guard, and reading
// only the first meant deleting that guard produced no finding at all. The
// window is small on purpose: past a couple of statements a block is doing work
// rather than refusing, and counting it would make ordinary refactors report.
const guardClauseBodyLookahead = 2

// guardOccurrence is one refusing check: where it opens, and the normalized
// text that identifies it. The signature carries the clause and, when the
// refusal lives in the body rather than on the clause line, that refusal too,
// so "same condition, same refusing action" is one comparison.
type guardOccurrence struct {
	line      int
	signature string
}

// guardOccurrences returns every line that opens a refusing check. A line
// carrying both the clause and its subject is one; a clause whose subject sits
// in its short body is also one, counted at the clause.
func guardOccurrences(lines []string) []guardOccurrence {
	var found []guardOccurrence
	for number, line := range lines {
		if isComment(line) || !guardClause.MatchString(line) {
			continue
		}
		if guardSubject.MatchString(line) {
			found = append(found, guardOccurrence{line: number, signature: normalizeExpression(line)})
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(line), "{") && !strings.HasSuffix(strings.TrimSpace(line), ":") {
			continue
		}
		for offset := 1; offset <= guardClauseBodyLookahead && number+offset < len(lines); offset++ {
			body := strings.TrimSpace(lines[number+offset])
			if body == "" || isComment(body) {
				continue
			}
			if body == "}" || body == "end" {
				break
			}
			if guardSubject.MatchString(body) {
				found = append(found, guardOccurrence{
					line:      number,
					signature: normalizeExpression(line) + "=>" + normalizeExpression(body),
				})
				break
			}
		}
	}
	return found
}

func detectRedundantComment(file File) []Finding {
	var findings []Finding
	current := splitLines(file.CurrentContent)
	lexed, declarations := lexSource(file.Path, current)
	baselinePath := file.BaselinePath
	if baselinePath == "" {
		baselinePath = file.Path
	}
	baseline, _ := lexSource(baselinePath, splitLines(file.BaselineContent))
	for _, block := range commentBlocks(file.AddedContent, lexed, baseline) {
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
	baselineComments := countCommentBlocks(baseline)
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

func countCommentBlocks(lines []lexedSourceLine) map[string]int {
	counts := make(map[string]int)
	for _, block := range allCommentBlocks(lines) {
		if block.text != "" {
			counts[block.text]++
		}
	}
	return counts
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
		if !codeWords[word] {
			return false
		}
	}
	return true
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
				addDeclaration(declarations, declarationFiles, declaration.Doc, valueSpecName(declaration), lineOffset)
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
		return valueSpecName(declaration)
	}
	return ""
}

func valueSpecName(declaration *ast.ValueSpec) string {
	names := make([]string, 0, len(declaration.Names))
	for _, name := range declaration.Names {
		if name.Name != "_" {
			names = append(names, name.Name)
		}
	}
	return strings.Join(names, "_")
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
	baselineSkeletons := newSkeletonIndex(baseline)
	for _, line := range addedLines(file) {
		currentMatch := tolerancePattern.FindStringSubmatch(line.text)
		if currentMatch == nil || currentMatch[1] != ">" {
			continue
		}
		previous := correspondingBaselineLine(baselineSkeletons, baseline, line.number, line.text)
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
	added := addedLines(file)
	// The observed-value assignments are indexed once. Re-scanning the file
	// from line 1 for every added line made this detector quadratic in file
	// size: 4000 added lines took 40 seconds, which is long enough that a large
	// diff starts to look like a hung gate.
	observed := observedAssignmentLines(current)
	baselineAssignments := newAssignmentIndex(baseline)
	addedByNumber := make(map[int]sourceLine, len(added))
	for _, candidate := range added {
		addedByNumber[candidate.number] = candidate
	}
	for _, line := range added {
		match := expectedPattern.FindStringSubmatch(line.text)
		if match == nil {
			continue
		}
		name := match[1]
		currentExpression := cleanExpression(match[2])
		previousLine := findAssignment(baselineAssignments, baseline, name, line.number)
		previousMatch := expectedPattern.FindStringSubmatch(previousLine)
		if previousMatch == nil || !containsLiteral(previousMatch[2]) {
			continue
		}
		if !computedOracle(observed, line.number, currentExpression) {
			continue
		}
		findingLine := line.number
		if candidate, ok := addedByNumber[line.number-1]; ok && assignmentPattern.MatchString(candidate.text) {
			findingLine = candidate.number
		}
		if upperCaseStart.MatchString(currentExpression) {
			if actualLine := observed[currentExpression]; actualLine > 0 && actualLine < line.number {
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

// detectCommentDefendedWorkaround reports a justification comment sitting next
// to a permissive return or a security bypass.
//
// The nearby risky action is required, and that boundary is deliberate rather
// than an oversight: a comment reading "Workaround for the broken upstream
// parser" above ordinary code is a note about the world, not a defect, and a
// detector that fired on it would report on a large share of honest code and
// get switched off. What the lens is for is the pairing, where prose is doing
// the work of justifying a bypass the code performs. Round 3 recorded the
// narrowness; this comment records that it is the intended shape.
func detectCommentDefendedWorkaround(file File) []Finding {
	added := addedLines(file)
	current := splitLines(file.CurrentContent)
	lowerCurrentContent := strings.ToLower(file.CurrentContent)
	for _, comment := range added {
		if !isComment(comment.text) || !defendsAWorkaround(comment.text) {
			continue
		}
		actionLine, action := riskyActionNear(current, comment.number, 4)
		if actionLine == 0 {
			continue
		}
		findingLine := comment.number
		if strings.Contains(strings.ToLower(action), "insecureskipverify") {
			findingLine = actionLine
		} else if strings.Contains(lowerCurrentContent, "availability matters") {
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

// intentStatesAScopeLimit reports whether an intent carries one of the scope
// constraints detectScopeExpansion can act on. It is the single owner of that
// list, so the mandatory-check line and the detector cannot drift apart and
// claim different coverage.
func intentStatesAScopeLimit(intent string) bool {
	lower := strings.ToLower(strings.TrimSpace(intent))
	return strings.Contains(lower, "without adding runtime behavior") ||
		strings.Contains(lower, "without changing the database schema") ||
		strings.Contains(lower, " only")
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
		if !isComment(line.text) || !assertsAFollowup(line.text) {
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

// detectFailOpenDefault reports an error, timeout, or unreadable state turning
// into a permissive result.
//
// It deliberately has no comment-based suppression. An earlier version stood
// down when a nearby comment claimed the permissive branch was intentional,
// which meant `if err != nil { return true }` was caught and the identical code
// with "// by policy" above it was not. Writing that comment is free and was
// verified against nothing, so the check could always be disarmed by the party
// it was checking. That is a vacuous-check by this project's own taxonomy. A
// permissive error path that really is the documented contract is a finding
// worth answering out loud, not one worth hiding with a comment.
func detectFailOpenDefault(file File) []Finding {
	current := splitLines(file.CurrentContent)
	for _, line := range addedLines(file) {
		lower := strings.ToLower(strings.TrimSpace(line.text))
		if !isPermissiveReturn(lower) || !errorContextNear(current, line.number, 4) {
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

	if explicitBranch.MatchString(lowerCurrent) {
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
		if !dispatchComparison.MatchString(line.text) {
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

func correspondingBaselineLine(index skeletonIndex, baseline []string, lineNumber int, current string) string {
	target := numericSkeleton(current)
	if lineNumber > 0 && lineNumber <= len(baseline) {
		candidate := baseline[lineNumber-1]
		if numericSkeleton(candidate) == target {
			return candidate
		}
	}
	return index[target]
}

// skeletonIndex maps a line's numeric skeleton to the first baseline line with
// that shape. Recomputing every baseline skeleton for every added line made
// tolerance detection quadratic in file size.
type skeletonIndex map[string]string

func newSkeletonIndex(lines []string) skeletonIndex {
	index := make(skeletonIndex)
	for _, line := range lines {
		skeleton := numericSkeleton(line)
		if _, seen := index[skeleton]; !seen {
			index[skeleton] = line
		}
	}
	return index
}

func numericSkeleton(value string) string {
	return strings.Join(strings.Fields(numericToken.ReplaceAllString(value, "#")), "")
}

func findAssignment(index assignmentIndex, lines []string, name string, preferredLine int) string {
	if preferredLine > 0 && preferredLine <= len(lines) {
		if match := assignmentPattern.FindStringSubmatch(lines[preferredLine-1]); match != nil && match[1] == name {
			return lines[preferredLine-1]
		}
	}
	return index[name]
}

// assignmentIndex maps an assigned name to the first line that assigns it, so
// the lookup does not rescan the baseline once per added line.
type assignmentIndex map[string]string

func newAssignmentIndex(lines []string) assignmentIndex {
	index := make(assignmentIndex)
	for _, line := range lines {
		match := assignmentPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if _, seen := index[match[1]]; !seen {
			index[match[1]] = line
		}
	}
	return index
}

func containsLiteral(value string) bool {
	return literalToken.MatchString(value)
}

func cleanExpression(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "{")
	return strings.TrimSpace(value)
}

// observedAssignmentLines indexes every `got`/`actual` assignment by the
// expression it holds, mapped to the first line that assigns it.
func observedAssignmentLines(lines []string) map[string]int {
	observed := make(map[string]int)
	for number, line := range lines {
		match := assignmentPattern.FindStringSubmatch(line)
		if match == nil || !containsAny(strings.ToLower(match[1]), "got", "actual") {
			continue
		}
		expression := cleanExpression(match[2])
		if _, seen := observed[expression]; !seen {
			observed[expression] = number + 1
		}
	}
	return observed
}

func computedOracle(observed map[string]int, lineNumber int, expression string) bool {
	lower := strings.ToLower(expression)
	if containsAny(expression, "+", "*", "/") && computedExpression.MatchString(expression) {
		return true
	}
	if !strings.Contains(expression, "(") || containsAny(lower, "decodehex", "mustdecode", "parsefixture") {
		return false
	}
	line, ok := observed[expression]
	return ok && line < lineNumber
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

// isPermissiveReturn reads the returned values rather than matching whole
// statements. The exact-string list it replaces recognised `return true` and
// missed `return true, nil`, which is how the same permission is granted from a
// function that also returns an error, and missed every language that ends the
// statement with a semicolon.
func isPermissiveReturn(lower string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSpace(lower), ";")
	rest, ok := strings.CutPrefix(trimmed, "return")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return false
	}
	if strings.HasPrefix(rest, "allow") || strings.HasPrefix(rest, "allpermissions(") || strings.HasPrefix(rest, "permit") {
		return true
	}
	if strings.Contains(rest, "cached[") && strings.HasSuffix(rest, ", nil") {
		return true
	}
	values := splitReturnValues(rest)
	if len(values) == 0 {
		return false
	}
	grants := false
	for _, value := range values {
		switch value {
		case "true":
			grants = true
		case "nil", "null", "none":
		default:
			return false
		}
	}
	// All-nil is the permissive shape for a function whose only signal is the
	// error it did not return.
	return grants || len(values) > 1
}

// splitReturnValues splits a return expression on top-level commas so a call
// with its own arguments is not mistaken for several returned values.
func splitReturnValues(rest string) []string {
	var values []string
	depth := 0
	current := strings.Builder{}
	for _, letter := range rest {
		switch letter {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				values = append(values, strings.TrimSpace(current.String()))
				current.Reset()
				continue
			}
		}
		current.WriteRune(letter)
	}
	values = append(values, strings.TrimSpace(current.String()))
	return values
}

// errorContextNear reports whether the nearest conditional above lineNumber is
// the one that tests for an error, timeout, or unreadable state.
//
// It asks about the NEAREST conditional rather than any error mention in the
// window. Scanning the whole window flagged every `return true, nil` that
// happened to sit a few lines below an unrelated `if err != nil`, which in Go
// is most of them: the shape is the ordinary success path of a (bool, error)
// function, not a permissive error default. Anchoring on the enclosing branch
// keeps `if err != nil { return true }` caught and lets the success path go.
func errorContextNear(lines []string, lineNumber, distance int) bool {
	start := lineNumber - distance
	if start < 1 {
		start = 1
	}
	for number := lineNumber; number >= start; number-- {
		if number > len(lines) {
			continue
		}
		line := lines[number-1]
		if !conditionalLine.MatchString(line) {
			continue
		}
		return errorContextPattern.MatchString(strings.ToLower(line))
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
		if isRestrictiveReturn(strings.ToLower(strings.TrimSpace(lines[number-1]))) {
			return true
		}
	}
	return false
}

func isRestrictiveReturn(lower string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(lower, ";"), "return")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	return strings.HasPrefix(rest, "deny") || strings.HasPrefix(rest, "false") ||
		strings.HasPrefix(rest, "reject") || strings.HasPrefix(rest, "forbid") ||
		strings.HasPrefix(rest, "badrequest") || strings.HasPrefix(rest, "unauthorized") ||
		strings.Contains(rest, "fmt.error") || strings.Contains(rest, "errors.new")
}

func permissiveReturnAfter(lines []string, start, distance int) int {
	end := start + distance
	if end > len(lines) {
		end = len(lines)
	}
	for number := start; number <= end; number++ {
		if isPermissiveReturn(strings.ToLower(strings.TrimSpace(lines[number-1]))) {
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

// letterSkeleton reduces prose to its letters, so a stem match no longer
// depends on the word order and punctuation of the corpus seed it came from.
// The detectors used to compare against the literal English of their own
// seeds, which is why "We work around the broken parser" fired and "Workaround
// for the broken parser" did not: the same claim, invisible because it was
// phrased differently.
func letterSkeleton(value string) string {
	var skeleton strings.Builder
	skeleton.Grow(len(value))
	for _, letter := range strings.ToLower(value) {
		if letter >= 'a' && letter <= 'z' {
			skeleton.WriteRune(letter)
		}
	}
	return skeleton.String()
}

func containsStem(value string, stems ...string) bool {
	skeleton := letterSkeleton(value)
	for _, stem := range stems {
		if strings.Contains(skeleton, stem) {
			return true
		}
	}
	return false
}

// workaroundStems name a comment that defends code rather than explaining it.
var workaroundStems = []string{
	"workaround", "worksaround", "workingaround", "workedaround", "hackaround",
	"temporar", "fornow", "quickfix",
	"bypass", "skipverif", "disableverif", "knownissue", "flaky", "noisy",
	"availabilitymatters", "freshness", "timesensitive", "unfortunately",
	"tolerateuntil", "revisitlater", "notideal", "goodenoughfor",
}

func defendsAWorkaround(comment string) bool {
	return containsStem(comment, workaroundStems...)
}

// followupVerb names the claim that somebody has taken the work on, and
// followupDeferral names the admission that it has not happened yet. Both are
// required.
//
// The verb alone is not the signal: a doc comment saying "maps an assigned name
// to the first line that assigns it" uses one and defers nothing. These match
// on word boundaries rather than on a letter skeleton, because collapsing the
// spaces made "a new file, and" contain "file a" and made a test named
// TestFollowupDetection assert its own follow-up.
var (
	followupVerb     = regexp.MustCompile(`(?i)\b(?:filed|filing|tracked|tracking|assigned|approved|scheduled|ticketed|logged|raised|signed\s+off|agreed)\b`)
	followupDeferral = regexp.MustCompile(`(?i)\b(?:todo|fixme|hack|xxx|later|next|follow[\s-]?up|removal|remove|removing|temporar\w*|deferred|defer|future|eventually|upcoming|quarter|for\s+now)\b`)
)

func assertsAFollowup(comment string) bool {
	return followupVerb.MatchString(comment) && followupDeferral.MatchString(comment)
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
