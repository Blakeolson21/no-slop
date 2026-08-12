package corpus

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/slop/leakscan"
	"github.com/kunchenguid/no-mistakes/internal/slop/precheck"
	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
	"github.com/kunchenguid/no-mistakes/internal/slop/testfloor"
)

var unifiedHunk = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ReplayOptions supplies non-expectation campaign inputs to deterministic
// replay checks.
type ReplayOptions struct {
	Intent string
	Thread *prose.Thread
}

// ReplayMandatory runs deterministic lens, leak, test-count, and
// outbound-prose checks over a recorded unified diff. It consumes no case
// expectations.
func ReplayMandatory(ctx context.Context, diff []byte, options ReplayOptions) ([]Finding, error) {
	files, err := replayFiles(diff)
	if err != nil {
		return nil, err
	}
	leakFiles := make([]leakscan.File, 0, len(files))
	precheckFiles := make([]precheck.File, 0, len(files))
	artifacts := make([]prose.Artifact, 0, len(files))
	baselineTests := make([]testfloor.File, 0, len(files))
	currentTests := make([]testfloor.File, 0, len(files))
	for _, file := range files {
		leakFiles = append(leakFiles, leakscan.File{Path: file.path, Content: renderReplayLines(file.added)})
		precheckFiles = append(precheckFiles, precheck.File{
			Path:            file.path,
			AddedContent:    renderReplayLines(file.added),
			BaselineContent: renderReplayLines(file.baseline),
			CurrentContent:  renderReplayLines(file.current),
		})
		artifacts = append(artifacts, prose.Artifact{Path: file.path, Content: renderReplayLines(file.current)})
		baselineTests = append(baselineTests, testfloor.File{Path: file.path, Content: renderReplayLines(file.baseline)})
		currentTests = append(currentTests, testfloor.File{Path: file.path, Content: renderReplayLines(file.current)})
	}
	var findings []Finding
	for _, finding := range precheck.Scan(precheckFiles, options.Intent) {
		findings = append(findings, Finding{
			Lens:        finding.Lens,
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	for _, finding := range leakscan.Scan(leakFiles, leakscan.Options{}).Findings {
		findings = append(findings, Finding{
			Lens:        "leak-identity-scan",
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	if floor := testfloor.Compare(baselineTests, currentTests); !floor.Passed {
		path, line := firstDeletedTest(files)
		findings = append(findings, Finding{
			Lens:        "test-capitulation",
			Path:        path,
			Line:        line,
			Description: fmt.Sprintf("test-count floor dropped from %d to %d", floor.Baseline, floor.Current),
		})
	}
	proseOptions := prose.Options{OutboundPaths: []string{"outbound/**"}}
	if options.Thread != nil {
		proseOptions.ThreadURL = "replay-fixture"
		proseOptions.ThreadReader = staticThreadReader{thread: *options.Thread}
	}
	proseFindings, err := prose.Check(ctx, artifacts, proseOptions)
	if err != nil {
		return nil, fmt.Errorf("replay mandatory prose checks: %w", err)
	}
	for _, finding := range proseFindings {
		findings = append(findings, Finding{
			Lens:        string(finding.Kind),
			Path:        finding.Path,
			Line:        finding.Line,
			Description: finding.Description,
		})
	}
	return findings, nil
}

type staticThreadReader struct{ thread prose.Thread }

func (reader staticThreadReader) Read(context.Context, string) (prose.Thread, error) {
	return reader.thread, nil
}

type replayFile struct {
	path     string
	added    map[int]string
	removed  map[int]string
	baseline map[int]string
	current  map[int]string
}

func replayFiles(diff []byte) ([]replayFile, error) {
	var files []replayFile
	var current *replayFile
	oldLine, newLine := 0, 0
	scanner := bufio.NewScanner(strings.NewReader(string(diff)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = nil
			oldLine, newLine = 0, 0
		case strings.HasPrefix(line, "--- "):
			continue
		case strings.HasPrefix(line, "+++ b/"):
			files = append(files, replayFile{
				path:     strings.TrimPrefix(line, "+++ b/"),
				added:    make(map[int]string),
				removed:  make(map[int]string),
				baseline: make(map[int]string),
				current:  make(map[int]string),
			})
			current = &files[len(files)-1]
			oldLine, newLine = 0, 0
		case strings.HasPrefix(line, "@@ "):
			if current == nil {
				return nil, fmt.Errorf("replay unified diff: hunk has no current file")
			}
			match := unifiedHunk.FindStringSubmatch(line)
			if match == nil {
				return nil, fmt.Errorf("replay unified diff: malformed hunk header %q", line)
			}
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[2])
			// The synthetic corpus abbreviates function-scoped hunks by putting
			// the opening source line only in the hunk section heading. Preserve
			// the source coordinates used when the expectation was authored.
			if lastMarker := strings.LastIndex(line, "@@"); lastMarker >= 0 && strings.TrimSpace(line[lastMarker+2:]) != "" {
				oldLine++
				newLine++
			}
		case current == nil || oldLine == 0 && newLine == 0:
			continue
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			content := strings.TrimPrefix(line, "+")
			current.added[newLine] = content
			current.current[newLine] = content
			newLine++
		case strings.HasPrefix(line, "-"):
			content := strings.TrimPrefix(line, "-")
			current.removed[oldLine] = content
			current.baseline[oldLine] = content
			oldLine++
		case strings.HasPrefix(line, " "):
			content := strings.TrimPrefix(line, " ")
			current.baseline[oldLine] = content
			current.current[newLine] = content
			oldLine++
			newLine++
		case strings.HasPrefix(line, `\ No newline`):
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("replay unified diff: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("replay unified diff: no changed files")
	}
	return files, nil
}

func firstDeletedTest(files []replayFile) (string, int) {
	for _, file := range files {
		indexes := make([]int, 0, len(file.removed))
		for index := range file.removed {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			line := strings.TrimSpace(file.removed[index])
			if strings.HasPrefix(line, "func Test") || strings.HasPrefix(line, "func Benchmark") || strings.HasPrefix(line, "func Example") || strings.HasPrefix(line, "def test_") || strings.Contains(line, "it(") || strings.Contains(line, "test(") {
				return file.path, index
			}
		}
	}
	return "", 0
}

func renderReplayLines(lines map[int]string) string {
	if len(lines) == 0 {
		return ""
	}
	indexes := make([]int, 0, len(lines))
	for index := range lines {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	rendered := make([]string, indexes[len(indexes)-1])
	for _, index := range indexes {
		rendered[index-1] = lines[index]
	}
	return strings.Join(rendered, "\n")
}
