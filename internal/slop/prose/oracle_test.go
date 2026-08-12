package prose_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
)

func TestCheckScansOnlyOutboundArtifactsForVoiceAndDashTells(t *testing.T) {
	t.Parallel()

	artifacts := []prose.Artifact{
		{Path: "notes/private.md", Content: "A crucial note \u2014 keep private."},
		{Path: "outbound/announcement.md", Content: "This crucial launch \u2014 changes review."},
		{Path: "outbound/data.json", Content: `{"note":"A crucial value \u2014 is data"}`},
		{Path: "draft.md", Content: "---\noutbound: true\n---\nA seamless workflow."},
	}
	findings, err := prose.Check(context.Background(), artifacts, prose.Options{
		OutboundPaths: []string{"outbound/**"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if countKind(findings, prose.EmDash) != 1 {
		t.Fatalf("findings = %+v, want one em-dash finding", findings)
	}
	if countKind(findings, prose.AITell) != 2 {
		t.Fatalf("findings = %+v, want two AI-tell findings", findings)
	}
	for _, finding := range findings {
		if finding.Path == "notes/private.md" {
			t.Fatalf("non-outbound artifact was scanned: %+v", finding)
		}
	}
}

func TestCheckRecomputesNumbersFromCitedJSONAndCSV(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evidence", "counts.json"), []byte(`{"passed":18,"failed":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evidence", "timings.csv"), []byte("run,seconds\na,4\nb,6\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := prose.Check(context.Background(), []prose.Artifact{{
		Path:    "outbound/report.md",
		Content: "evidence/counts.json shows 20 total checks.\nevidence/timings.csv averages 5 seconds.\nevidence/counts.json shows 99 failures.",
	}}, prose.Options{
		OutboundPaths: []string{"outbound/**"},
		EvidenceRoot:  root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(findings, prose.EvidenceMismatch) != 1 {
		t.Fatalf("findings = %+v, want only the unsupported 99 claim", findings)
	}
	if findings[len(findings)-1].Line != 3 {
		t.Fatalf("mismatch line = %d, want 3", findings[len(findings)-1].Line)
	}
}

func TestCheckBindsClaimsToNearestCitationAndNamedOperation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evidence", "counts.json"), []byte(`[18,2]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evidence", "timings.csv"), []byte("seconds\n4\n6\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := prose.Check(context.Background(), []prose.Artifact{{
		Path: "outbound/report.md",
		Content: "evidence/counts.json reports 20 total checks; evidence/timings.csv reports a 5 average.\n" +
			"evidence/counts.json reports 900 without an operation.\n" +
			"evidence/counts.json reports a 900 percent rate.",
	}}, prose.Options{OutboundPaths: []string{"outbound/**"}, EvidenceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(findings, prose.EvidenceMismatch) != 1 {
		t.Fatalf("findings = %+v, want only unsupported direct 900 claim", findings)
	}
	if findings[len(findings)-1].Line != 2 {
		t.Fatalf("mismatch line = %d, want 2", findings[len(findings)-1].Line)
	}
}

type stubThreadReader struct {
	thread prose.Thread
	err    error
}

func (s stubThreadReader) Read(context.Context, string) (prose.Thread, error) {
	return s.thread, s.err
}

func TestCheckFailsClosedForClosedThreadAndDuplicateClaim(t *testing.T) {
	t.Parallel()

	findings, err := prose.Check(context.Background(), []prose.Artifact{{
		Path:    "outbound/comment.md",
		Content: "The retry path converts an unknown provider state into success.",
	}}, prose.Options{
		OutboundPaths: []string{"outbound/**"},
		ThreadURL:     "https://github.com/example/project/issues/42",
		ThreadReader: stubThreadReader{thread: prose.Thread{
			Open:     false,
			Comments: []string{"An unknown provider state is converted into success by the retry path."},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(findings, prose.ThreadClosed) != 1 {
		t.Fatalf("findings = %+v, want closed-thread finding", findings)
	}
	if countKind(findings, prose.DuplicateClaim) != 1 {
		t.Fatalf("findings = %+v, want duplicate-claim finding", findings)
	}
}

func TestCheckFindsDuplicateParagraphInsideMultiPointDraft(t *testing.T) {
	t.Parallel()

	findings, err := prose.Check(context.Background(), []prose.Artifact{{
		Path: "outbound/comment.md",
		Content: "The build now emits a compact summary for each package.\n\n" +
			"The retry path converts an unknown provider state into success.\n\n" +
			"The documentation includes a separate migration example.",
	}}, prose.Options{
		OutboundPaths: []string{"outbound/**"},
		ThreadURL:     "https://github.com/example/project/issues/42",
		ThreadReader: stubThreadReader{thread: prose.Thread{
			Open:     true,
			Comments: []string{"An unknown provider state is converted into success by the retry path."},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(findings, prose.DuplicateClaim) != 1 {
		t.Fatalf("findings = %+v, want duplicate claim for one paragraph", findings)
	}
}

func TestCheckFailsClosedWhenLiveThreadCannotBeRead(t *testing.T) {
	t.Parallel()

	_, err := prose.Check(context.Background(), []prose.Artifact{{
		Path:    "outbound/comment.md",
		Content: "A new claim.",
	}}, prose.Options{
		OutboundPaths: []string{"outbound/**"},
		ThreadURL:     "https://github.com/example/project/pull/7",
		ThreadReader:  stubThreadReader{err: context.DeadlineExceeded},
	})
	if err == nil {
		t.Fatal("expected unreadable live thread to fail closed")
	}
}

func countKind(findings []prose.Finding, kind prose.Kind) int {
	total := 0
	for _, finding := range findings {
		if finding.Kind == kind {
			total++
		}
	}
	return total
}
