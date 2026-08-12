// Package prose validates text artifacts intended for outbound publication.
package prose

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/slop/pathmatch"
)

// Kind identifies a prose-oracle finding class.
type Kind string

const (
	AITell             Kind = "ai-tell"
	EmDash             Kind = "em-dash"
	ThreadClosed       Kind = "thread-closed"
	DuplicateClaim     Kind = "duplicate-claim"
	EvidenceMismatch   Kind = "evidence-mismatch"
	EvidenceUnreadable Kind = "evidence-unreadable"
)

// Artifact is one changed text file.
type Artifact struct {
	Path    string
	Content string
}

// Options configures outbound recognition and optional live/evidence checks.
type Options struct {
	OutboundPaths []string
	AITellWords   []string
	ThreadURL     string
	ThreadReader  ThreadReader
	EvidenceRoot  string
}

// Thread is the live state needed before posting outbound text.
type Thread struct {
	Open     bool
	Comments []string
}

// ThreadReader reads issue or pull-request state and comments.
type ThreadReader interface {
	Read(context.Context, string) (Thread, error)
}

// Finding is one deterministic prose-oracle result.
type Finding struct {
	Kind        Kind
	Path        string
	Line        int
	Description string
}

var defaultAITells = []string{
	"crucial",
	"delve",
	"game-changer",
	"in today's fast-paced",
	"seamless",
	"tapestry",
	"transformative",
	"unlock",
}

var outboundFrontMatter = regexp.MustCompile(`(?mi)^\s*(?:outbound|noslop)\s*:\s*(?:true|outbound)\s*$`)

// Check runs every relevant oracle over outbound artifacts.
func Check(ctx context.Context, artifacts []Artifact, opts Options) ([]Finding, error) {
	words := append([]string(nil), defaultAITells...)
	words = append(words, opts.AITellWords...)

	var findings []Finding
	var outbound []Artifact
	for _, artifact := range artifacts {
		if !isOutbound(artifact, opts.OutboundPaths) {
			continue
		}
		outbound = append(outbound, artifact)
		for index, line := range strings.Split(artifact.Content, "\n") {
			if strings.ContainsRune(line, '\u2014') {
				findings = append(findings, Finding{
					Kind:        EmDash,
					Path:        artifact.Path,
					Line:        index + 1,
					Description: "outbound text contains an em dash",
				})
			}
			lower := strings.ToLower(line)
			for _, word := range words {
				if containsPhrase(lower, strings.ToLower(strings.TrimSpace(word))) {
					findings = append(findings, Finding{
						Kind:        AITell,
						Path:        artifact.Path,
						Line:        index + 1,
						Description: fmt.Sprintf("outbound text contains AI-tell phrase %q", word),
					})
				}
			}
			evidenceFindings := checkEvidenceLine(artifact.Path, index+1, line, opts.EvidenceRoot)
			findings = append(findings, evidenceFindings...)
		}
	}

	if opts.ThreadURL != "" && len(outbound) > 0 {
		if opts.ThreadReader == nil {
			return nil, fmt.Errorf("check live thread: no thread reader configured")
		}
		thread, err := opts.ThreadReader.Read(ctx, opts.ThreadURL)
		if err != nil {
			return nil, fmt.Errorf("check live thread: %w", err)
		}
		if !thread.Open {
			findings = append(findings, Finding{
				Kind:        ThreadClosed,
				Description: "target issue or pull request is not open",
			})
		}
		for _, artifact := range outbound {
			for _, comment := range thread.Comments {
				if samePoint(artifact.Content, comment) {
					findings = append(findings, Finding{
						Kind:        DuplicateClaim,
						Path:        artifact.Path,
						Description: "an existing comment already makes substantially the same claim",
					})
					break
				}
			}
		}
	}
	return findings, nil
}

var evidencePathPattern = regexp.MustCompile(`(?:^|[\s(` + "`" + `\[])([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\.(?:json|csv))\b`)
var numberPattern = regexp.MustCompile(`[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?`)

func checkEvidenceLine(artifactPath string, lineNumber int, line, root string) []Finding {
	if root == "" {
		return nil
	}
	pathMatches := evidencePathPattern.FindAllStringSubmatchIndex(line, -1)
	if len(pathMatches) == 0 {
		return nil
	}
	claims := claimedNumbers(line, pathMatches)
	if len(claims) == 0 {
		return nil
	}

	var findings []Finding
	assignments := assignClaimsToCitations(pathMatches, claims)
	for citationIndex, match := range pathMatches {
		citation := line[match[2]:match[3]]
		assigned := assignments[citationIndex]
		if len(assigned) == 0 {
			continue
		}
		values, err := evidenceValues(root, citation)
		if err != nil {
			findings = append(findings, Finding{
				Kind:        EvidenceUnreadable,
				Path:        artifactPath,
				Line:        lineNumber,
				Description: fmt.Sprintf("cited evidence file %q could not be verified: %v", citation, err),
			})
			continue
		}
		for _, claim := range assigned {
			supported := numbersForOperation(values, detectOperation(line, claim))
			if !containsNumber(supported, claim.value) {
				findings = append(findings, Finding{
					Kind:        EvidenceMismatch,
					Path:        artifactPath,
					Line:        lineNumber,
					Description: fmt.Sprintf("a cited number does not appear in or derive from %q", citation),
				})
			}
		}
	}
	return findings
}

type numericClaim struct {
	value float64
	start int
	end   int
}

func claimedNumbers(line string, paths [][]int) []numericClaim {
	var claims []numericClaim
	for _, match := range numberPattern.FindAllStringIndex(line, -1) {
		overlapsPath := false
		for _, pathMatch := range paths {
			if match[0] < pathMatch[3] && match[1] > pathMatch[2] {
				overlapsPath = true
				break
			}
		}
		if overlapsPath {
			continue
		}
		value, err := strconv.ParseFloat(line[match[0]:match[1]], 64)
		if err == nil {
			claims = append(claims, numericClaim{value: value, start: match[0], end: match[1]})
		}
	}
	return claims
}

func assignClaimsToCitations(paths [][]int, claims []numericClaim) map[int][]numericClaim {
	assignments := make(map[int][]numericClaim)
	for _, claim := range claims {
		closestIndex := 0
		closestDistance := spanDistance(claim.start, claim.end, paths[0][2], paths[0][3])
		for index := 1; index < len(paths); index++ {
			distance := spanDistance(claim.start, claim.end, paths[index][2], paths[index][3])
			if distance < closestDistance {
				closestIndex = index
				closestDistance = distance
			}
		}
		assignments[closestIndex] = append(assignments[closestIndex], claim)
	}
	return assignments
}

func spanDistance(leftStart, leftEnd, rightStart, rightEnd int) int {
	if leftEnd <= rightStart {
		return rightStart - leftEnd
	}
	if rightEnd <= leftStart {
		return leftStart - rightEnd
	}
	return 0
}

func evidenceValues(root, citation string) ([]float64, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(citation))
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("path escapes evidence root")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(target)) {
	case ".json":
		var value any
		if err := json.Unmarshal(content, &value); err != nil {
			return nil, err
		}
		var numbers []float64
		collectJSONNumbers(value, &numbers)
		return numbers, nil
	case ".csv":
		rows, err := csv.NewReader(strings.NewReader(string(content))).ReadAll()
		if err != nil {
			return nil, err
		}
		var numbers []float64
		for _, row := range rows {
			for _, cell := range row {
				if number, err := strconv.ParseFloat(strings.TrimSpace(cell), 64); err == nil {
					numbers = append(numbers, number)
				}
			}
		}
		return numbers, nil
	default:
		return nil, fmt.Errorf("unsupported evidence format")
	}
}

func collectJSONNumbers(value any, numbers *[]float64) {
	switch typed := value.(type) {
	case float64:
		*numbers = append(*numbers, typed)
	case []any:
		for _, item := range typed {
			collectJSONNumbers(item, numbers)
		}
	case map[string]any:
		for _, item := range typed {
			collectJSONNumbers(item, numbers)
		}
	}
}

type evidenceOperation string

const (
	operationDirect  evidenceOperation = "direct"
	operationSum     evidenceOperation = "sum"
	operationCount   evidenceOperation = "count"
	operationAverage evidenceOperation = "average"
	operationMinimum evidenceOperation = "minimum"
	operationMaximum evidenceOperation = "maximum"
	operationRatio   evidenceOperation = "ratio"
	operationPercent evidenceOperation = "percent"
)

var evidenceOperations = []struct {
	kind    evidenceOperation
	pattern *regexp.Regexp
}{
	{operationSum, regexp.MustCompile(`(?i)\b(?:total|sum|combined)\b`)},
	{operationCount, regexp.MustCompile(`(?i)\b(?:count|rows?|records?|entries|items|values)\b`)},
	{operationAverage, regexp.MustCompile(`(?i)\b(?:average|averages|averaged|avg|mean)\b`)},
	{operationMinimum, regexp.MustCompile(`(?i)\b(?:minimum|min|lowest|smallest)\b`)},
	{operationMaximum, regexp.MustCompile(`(?i)\b(?:maximum|max|highest|largest)\b`)},
	{operationRatio, regexp.MustCompile(`(?i)\b(?:ratio|quotient)\b`)},
	{operationPercent, regexp.MustCompile(`(?i)\b(?:percent|percentage|rate)\b|%`)},
}

func detectOperation(line string, claim numericClaim) evidenceOperation {
	scrubbed := []byte(line)
	for _, match := range evidencePathPattern.FindAllStringSubmatchIndex(line, -1) {
		for index := match[2]; index < match[3]; index++ {
			scrubbed[index] = ' '
		}
	}
	closestKind := operationDirect
	closestDistance := len(line) + 1
	for _, operation := range evidenceOperations {
		for _, match := range operation.pattern.FindAllIndex(scrubbed, -1) {
			distance := spanDistance(claim.start, claim.end, match[0], match[1])
			if distance < closestDistance {
				closestKind = operation.kind
				closestDistance = distance
			}
		}
	}
	return closestKind
}

func numbersForOperation(values []float64, operation evidenceOperation) []float64 {
	if len(values) == 0 {
		return nil
	}
	derived := append([]float64(nil), values...)
	sum := 0.0
	minimum, maximum := values[0], values[0]
	for _, value := range values {
		sum += value
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	switch operation {
	case operationSum:
		derived = append(derived, sum)
	case operationCount:
		derived = append(derived, float64(len(values)))
	case operationAverage:
		derived = append(derived, sum/float64(len(values)))
	case operationMinimum:
		derived = append(derived, minimum)
	case operationMaximum:
		derived = append(derived, maximum)
	case operationRatio, operationPercent:
		for _, numerator := range values {
			for _, denominator := range values {
				if denominator == 0 {
					continue
				}
				ratio := numerator / denominator
				if operation == operationPercent {
					ratio *= 100
				}
				derived = append(derived, ratio)
			}
		}
	}
	return derived
}

func containsNumber(values []float64, claim float64) bool {
	for _, value := range values {
		tolerance := math.Max(1e-9, math.Abs(claim)*1e-6)
		if math.Abs(value-claim) <= tolerance {
			return true
		}
	}
	return false
}

var claimToken = regexp.MustCompile(`[a-z0-9]+`)

var claimStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "in": true, "into": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "that": true,
	"the": true, "this": true, "to": true, "was": true, "with": true,
}

func sameClaim(left, right string) bool {
	a := meaningfulTokens(left)
	b := meaningfulTokens(right)
	if len(a) < 4 || len(b) < 4 {
		return false
	}
	intersection := 0
	for token := range a {
		if b[token] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	return union > 0 && float64(intersection)/float64(union) >= 0.7
}

func samePoint(left, right string) bool {
	for _, leftClaim := range claimSegments(left) {
		for _, rightClaim := range claimSegments(right) {
			if sameClaim(leftClaim, rightClaim) {
				return true
			}
		}
	}
	return false
}

func claimSegments(value string) []string {
	var paragraphs []string
	var paragraph strings.Builder
	flush := func() {
		if paragraph.Len() == 0 {
			return
		}
		paragraphs = append(paragraphs, paragraph.String())
		paragraph.Reset()
	}
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			flush()
			paragraphs = append(paragraphs, strings.TrimSpace(trimmed[2:]))
			continue
		}
		if paragraph.Len() > 0 {
			paragraph.WriteByte(' ')
		}
		paragraph.WriteString(trimmed)
	}
	flush()

	var claims []string
	for _, paragraph := range paragraphs {
		for _, claim := range strings.FieldsFunc(paragraph, func(r rune) bool {
			return r == '.' || r == '!' || r == '?'
		}) {
			if claim = strings.TrimSpace(claim); claim != "" {
				claims = append(claims, claim)
			}
		}
	}
	return claims
}

func meaningfulTokens(value string) map[string]bool {
	tokens := make(map[string]bool)
	for _, token := range claimToken.FindAllString(strings.ToLower(value), -1) {
		if len(token) >= 3 && !claimStopWords[token] {
			tokens[token] = true
		}
	}
	return tokens
}

func isOutbound(artifact Artifact, patterns []string) bool {
	extension := strings.ToLower(filepath.Ext(artifact.Path))
	if extension != ".md" && extension != ".mdx" {
		return false
	}
	if outboundFrontMatter.MatchString(frontMatter(artifact.Content)) {
		return true
	}
	for _, pattern := range patterns {
		if pathmatch.Match(artifact.Path, pattern) {
			return true
		}
	}
	return false
}

func frontMatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func containsPhrase(line, phrase string) bool {
	if phrase == "" {
		return false
	}
	index := strings.Index(line, phrase)
	if index < 0 {
		return false
	}
	beforeOK := index == 0 || !isWordByte(line[index-1])
	after := index + len(phrase)
	afterOK := after == len(line) || !isWordByte(line[after])
	return beforeOK && afterOK
}

func isWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}
