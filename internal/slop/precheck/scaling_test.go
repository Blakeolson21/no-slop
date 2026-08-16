package precheck_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/slop/precheck"
)

// TestScanStaysLinearInFileSize is a ceiling, not a benchmark.
//
// The detectors rescanned the file from line 1 for every added line, so cost
// grew with the square of the diff: 1000 added lines took 2.5 seconds, 2000
// took 9.5, and 4000 took 40. Forty seconds of silence on one file reads like a
// hung gate, which is how a slow check becomes a skipped one. Indexing the
// lookups made the same input take tens of milliseconds. The bound below is
// two orders of magnitude above that, so it fails only if the quadratic
// behavior comes back, never because a machine was busy.
func TestScanStaysLinearInFileSize(t *testing.T) {
	t.Parallel()

	const lines = 4000
	var content strings.Builder
	for index := 0; index < lines; index++ {
		fmt.Fprintf(&content, "\twant%d := compute(%d)\n", index, index)
	}
	body := content.String()

	start := time.Now()
	precheck.Scan([]precheck.File{{
		Path:            "widget_test.go",
		AddedContent:    body,
		BaselineContent: body,
		CurrentContent:  body,
	}}, "")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("scanning %d added lines took %s, which is the quadratic shape returning", lines, elapsed)
	}
}
