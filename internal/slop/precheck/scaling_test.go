package precheck_test

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/slop/precheck"
)

// TestScanStaysLinearInFileSize is a shape check, not a benchmark.
//
// The detectors rescanned the file from line 1 for every added line, so cost
// grew with the square of the diff: 1000 added lines took 2.5 seconds, 2000
// took 9.5, and 4000 took 40. Forty seconds of silence on one file reads like a
// hung gate, which is how a slow check becomes a skipped one. Indexing the
// lookups made the same input take tens of milliseconds.
//
// The first version of this test was a 5-second wall-clock ceiling whose own
// comment claimed it "fails only if the quadratic behavior comes back, never
// because a machine was busy". That was not true and the round-5 reviewer
// caught it failing at 5.06s inside a saturated `go test -race ./...` sweep,
// where the same call takes 2.03s run alone. A wall-clock ceiling measures the
// machine as much as the code, and a test that fails on a busy CI runner
// teaches people to re-run it, which is how a real regression gets waved
// through.
//
// So the test measures the SHAPE instead: quadrupling the input must not
// multiply the cost by anything like sixteen. Load divides out of a ratio only
// when both halves met the same machine, so the two sizes are timed adjacently,
// the ratio is taken within each pair, and the best pair wins. A run fast
// enough that no quadratic version could have produced it draws no conclusion
// at all, and a generous absolute ceiling still catches a wedge.
func TestScanStaysLinearInFileSize(t *testing.T) {
	t.Parallel()

	const small = 1000
	const large = 4000
	const trials = 4

	smallBody := generatedTestBody(small)
	largeBody := generatedTestBody(large)

	// The two sizes are measured ADJACENTLY and the ratio is taken per pair,
	// then the best pair wins. Measuring all the small runs and then all the
	// large ones is what made the first version of this comparison flaky in its
	// own right: a load spike during the large phase inflated only the large
	// number, and a saturated sweep turned a true factor of 4.2 into 9.2.
	// Contention only divides out of a ratio when both halves met the same
	// machine, and the closest this can get to that is measuring them back to
	// back and discarding the pairs that were interrupted.
	bestRatio := math.Inf(1)
	var bestSmall, bestLarge time.Duration
	for trial := 0; trial < trials; trial++ {
		smallCost := scanCost(smallBody)
		largeCost := scanCost(largeBody)
		if smallCost <= 0 {
			continue
		}
		if ratio := float64(largeCost) / float64(smallCost); ratio < bestRatio {
			bestRatio, bestSmall, bestLarge = ratio, smallCost, largeCost
		}
	}

	// An absolute backstop far above any plausible instrumented run, so a
	// genuinely wedged detector fails here rather than timing out the package.
	// The quadratic version took 40 seconds at this input size uninstrumented,
	// and the linear one takes about 36ms, or 820ms under -race.
	if bestLarge > 30*time.Second {
		t.Fatalf("scanning %d added lines took %s, which is not a busy machine", large, bestLarge)
	}
	// Below this the ratio is measuring timer granularity rather than the
	// algorithm, and the quadratic shape could not reach it: it took 2.5
	// seconds at a quarter of this input.
	if bestLarge < 5*time.Millisecond || math.IsInf(bestRatio, 1) {
		return
	}
	// Four times the input. Linear is about 4 and quadratic is about 16, so 9
	// sits between them on the log scale and separates the two SHAPES rather
	// than pinning a constant factor the machine gets a vote in.
	if bestRatio > 9 {
		t.Fatalf("scanning %d lines took %s and %d lines took %s in the same pair, a factor of %.1f for 4x the input, which is the quadratic shape returning",
			small, bestSmall, large, bestLarge, bestRatio)
	}
}

// scanCost times one scan. It is the smallest unit the pairing above compares,
// so it deliberately does no averaging of its own.
func scanCost(body string) time.Duration {
	start := time.Now()
	precheck.Scan([]precheck.File{{
		Path:            "widget_test.go",
		AddedContent:    body,
		BaselineContent: body,
		CurrentContent:  body,
	}}, "")
	return time.Since(start)
}

func generatedTestBody(lines int) string {
	var content strings.Builder
	for index := 0; index < lines; index++ {
		fmt.Fprintf(&content, "\twant%d := compute(%d)\n", index, index)
	}
	return content.String()
}
