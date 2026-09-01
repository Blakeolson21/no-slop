package convergence

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Finding-class identity across rounds.
//
// Pipeline lineage identifies an exact carried finding, but it is insufficient
// for convergence-class identity: the observed ladder failure reported one
// defect class ("env-file parsing semantics disagree with docker compose") as
// three distinct lineages in three different files. Class identity therefore
// comes from content. Each finding is reduced to a normalized token set over its
// category and description (lowercased, lightly stemmed, stopwords and numbers
// dropped), and findings whose token sets substantially overlap are grouped.
//
// Similarity is the overlap coefficient (|A∩B| / min(|A|,|B|)) rather than
// Jaccard: reworded findings of the same class share a stable core vocabulary
// but differ wildly in length, and Jaccard punishes that length difference
// enough to split real recurrences. A minimum shared-token floor keeps short
// generic descriptions from over-merging.

const (
	classOverlapThreshold = 0.5
	classMinSharedTokens  = 3
)

type classEntry struct {
	finding types.Finding
	round   int
}

type class struct {
	members    []map[string]struct{}
	category   string
	rounds     map[int]struct{}
	files      []string
	fileSet    map[string]struct{}
	tokenCount map[string]int
	firstRound int
	firstIndex int
}

// recurringClasses groups findings across rounds by content identity and
// returns the classes seen in two or more distinct rounds, in first-seen
// order. Labeling is telemetry only: it never suppresses a finding.
func recurringClasses(entries []classEntry) []RecurringClass {
	var classes []*class
	for i, entry := range entries {
		tokens := findingTokens(entry.finding)
		if len(tokens) == 0 {
			continue
		}
		best := -1
		bestScore := 0.0
		for ci, c := range classes {
			if conflictingCategories(c.category, entry.finding.Category) {
				continue
			}
			score := c.bestOverlap(tokens)
			if score > bestScore {
				bestScore = score
				best = ci
			}
		}
		if best >= 0 && bestScore >= classOverlapThreshold {
			classes[best].add(entry, tokens)
			continue
		}
		c := &class{
			rounds:     map[int]struct{}{},
			fileSet:    map[string]struct{}{},
			tokenCount: map[string]int{},
			firstRound: entry.round,
			firstIndex: i,
			category:   strings.ToLower(strings.TrimSpace(entry.finding.Category)),
		}
		c.add(entry, tokens)
		classes = append(classes, c)
	}

	var out []RecurringClass
	for i, c := range classes {
		if len(c.rounds) < 2 {
			continue
		}
		rounds := make([]int, 0, len(c.rounds))
		for r := range c.rounds {
			rounds = append(rounds, r)
		}
		sort.Ints(rounds)
		out = append(out, RecurringClass{
			Label:  c.label(i),
			Rounds: rounds,
			Files:  c.files,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rounds[0] != out[j].Rounds[0] {
			return out[i].Rounds[0] < out[j].Rounds[0]
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func conflictingCategories(a, b string) bool {
	b = strings.ToLower(strings.TrimSpace(b))
	return a != "" && b != "" && a != b
}

func (c *class) add(entry classEntry, tokens map[string]struct{}) {
	c.members = append(c.members, tokens)
	c.rounds[entry.round] = struct{}{}
	if entry.finding.File != "" {
		if _, seen := c.fileSet[entry.finding.File]; !seen {
			c.fileSet[entry.finding.File] = struct{}{}
			c.files = append(c.files, entry.finding.File)
		}
	}
	for t := range tokens {
		c.tokenCount[t]++
	}
	if c.category == "" {
		c.category = strings.ToLower(strings.TrimSpace(entry.finding.Category))
	}
}

// bestOverlap is the maximum overlap coefficient between the candidate token
// set and any existing member. Comparing against members rather than the class
// union keeps a growing class from inflating its own similarity.
func (c *class) bestOverlap(tokens map[string]struct{}) float64 {
	best := 0.0
	for _, member := range c.members {
		small, large := tokens, member
		if len(member) < len(small) {
			small, large = member, small
		}
		if len(small) == 0 {
			continue
		}
		shared := 0
		for t := range small {
			if _, ok := large[t]; ok {
				shared++
			}
		}
		if shared < classMinSharedTokens {
			continue
		}
		if score := float64(shared) / float64(len(small)); score > best {
			best = score
		}
	}
	return best
}

// label synthesizes a short content-derived slug for the class: the tokens
// shared by the most members, most frequent first, alphabetical on ties.
func (c *class) label(index int) string {
	type tokenStat struct {
		token string
		count int
	}
	stats := make([]tokenStat, 0, len(c.tokenCount))
	for t, n := range c.tokenCount {
		stats = append(stats, tokenStat{token: t, count: n})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].count != stats[j].count {
			return stats[i].count > stats[j].count
		}
		return stats[i].token < stats[j].token
	})
	majority := len(c.members)/2 + 1
	var parts []string
	for _, s := range stats {
		if s.count < majority {
			break
		}
		parts = append(parts, s.token)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("class-%d", index+1)
	}
	return strings.Join(parts, "-")
}

var classStopwords = map[string]struct{}{}

func init() {
	for _, w := range []string{
		"the", "and", "for", "with", "that", "this", "from", "not", "are",
		"was", "were", "will", "would", "could", "should", "must", "may",
		"might", "can", "has", "have", "had", "been", "being", "its", "also",
		"only", "but", "into", "over", "than", "then", "when", "where",
		"which", "while", "each", "other", "same", "still", "never", "always",
		"without", "between", "before", "after", "because", "there", "their",
		"they", "them", "does", "did", "done", "get", "got", "one", "two",
		"all", "any", "out", "off", "per", "via", "these", "those", "such",
		"more", "most", "less", "least", "very", "much", "many", "some",
		"instead", "rather", "about", "against", "during", "under", "above",
		"below", "here", "how", "why", "what", "who", "whose", "you", "your",
		"it", "its", "is", "be", "an", "as", "at", "by", "do", "if", "in",
		"of", "on", "or", "so", "to", "up", "we", "no", "yes",
	} {
		classStopwords[w] = struct{}{}
	}
}

// findingTokens normalizes a finding's category and description into a token
// set: lowercase, split on non-alphanumeric runs, lightly stemmed, with
// stopwords, numbers, and very short tokens dropped. The file path field is
// deliberately not an input: class identity must survive a defect moving
// between files.
func findingTokens(f types.Finding) map[string]struct{} {
	text := strings.ToLower(f.Category + " " + f.Description)
	tokens := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		token := b.String()
		b.Reset()
		if len(token) < 3 || allDigits(token) {
			return
		}
		if _, stop := classStopwords[token]; stop {
			return
		}
		token = stem(token)
		if _, stop := classStopwords[token]; stop {
			return
		}
		if len(token) < 3 {
			return
		}
		tokens[token] = struct{}{}
	}
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

// stem strips a handful of common English suffixes so trivially inflected
// forms ("parses", "parsing", "quoted", "quoting") collapse to one token.
// Every normalization after the suffix cut runs on the base form too:
// "adding" loses its doubled consonant through the longer suffix, so bare
// "add" must lose it as well or the base is orphaned from its own
// inflections. Terminal e is normalized for the same reason. It is
// deliberately crude: identity only needs stable collapsing, not linguistic
// correctness.
func stem(token string) string {
	if rest, ok := strings.CutSuffix(token, "ies"); ok && len(rest) >= 2 {
		return rest + "y"
	}
	for _, suffix := range []string{"ing", "ed", "es", "s"} {
		if rest, ok := strings.CutSuffix(token, suffix); ok && len(rest) >= 3 {
			token = rest
			break
		}
	}
	token = collapseDoubledConsonant(token)
	if rest, ok := strings.CutSuffix(token, "e"); ok && len(rest) >= 3 {
		return rest
	}
	return token
}

func collapseDoubledConsonant(token string) string {
	if len(token) < 2 || token[len(token)-1] != token[len(token)-2] {
		return token
	}
	switch token[len(token)-1] {
	case 'b', 'd', 'f', 'g', 'm', 'n', 'p', 'r', 't':
		return token[:len(token)-1]
	default:
		return token
	}
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}
