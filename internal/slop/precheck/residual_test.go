package precheck_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/precheck"
)

// The round-3 review left two residual notes on R9. Both are about coverage the
// mandatory-check line claimed and the detectors did not have.

// TestDeletingAnInputGuardThatReturnsANamedErrorIsSeen closes the first. The
// guard subject needed a standalone "err" token, so `return errBad` matched
// nothing and removing an input guard that refused with a named error value
// produced no finding at all.
func TestDeletingAnInputGuardThatReturnsANamedErrorIsSeen(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name     string
		baseline string
	}{
		{
			name:     "camel case error value",
			baseline: "package svc\n\nfunc Handle(user string) error {\n\tif user == \"\" {\n\t\treturn errBad\n\t}\n\treturn nil\n}\n",
		},
		{
			name:     "exported error value",
			baseline: "package svc\n\nfunc Handle(user string) error {\n\tif user == \"\" {\n\t\treturn ErrForbidden\n\t}\n\treturn nil\n}\n",
		},
		{
			name:     "qualified error value",
			baseline: "package svc\n\nfunc Handle(user string) error {\n\tif user == \"\" {\n\t\treturn svc.ErrForbidden\n\t}\n\treturn nil\n}\n",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			result := precheck.Scan([]precheck.File{{
				Path:            "internal/svc/handler.go",
				BaselineContent: probe.baseline,
				CurrentContent:  "package svc\n\nfunc Handle(user string) error {\n\treturn nil\n}\n",
			}}, "")
			found := false
			for _, finding := range result.Findings {
				if finding.Lens == "fail-open-default" && strings.Contains(finding.Description, "refusing checks dropped") {
					found = true
				}
			}
			if !found {
				t.Fatalf("deleting the guard produced no finding: %+v", result.Findings)
			}
		})
	}
}

// TestConsolidatingLengthChecksStillReportsNothing is the false-positive
// control the widened subject must not break. An earlier widening of this same
// regex had to be stood back down for exactly this shape.
func TestConsolidatingLengthChecksStillReportsNothing(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{{
		Path:            "internal/svc/handler.go",
		BaselineContent: "package svc\n\nfunc Handle(a, b []int) int {\n\tif len(a) != 0 {\n\t\treturn 1\n\t}\n\tif len(b) != 0 {\n\t\treturn 2\n\t}\n\treturn 0\n}\n",
		CurrentContent:  "package svc\n\nfunc Handle(a, b []int) int {\n\tif len(a) != 0 || len(b) != 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
	}}, "")
	for _, finding := range result.Findings {
		if finding.Lens == "fail-open-default" {
			t.Fatalf("an ordinary consolidation reported a removed guard: %+v", finding)
		}
	}
}

// TestScopeExpansionReportsItselfUnarmedOnAnIntentItCannotAct is the second
// residual. The detector fires only on an intent that states a scope limit, so
// an intent of "stuff" left the check reporting armed while it was structurally
// unable to find anything. The intent is author-supplied, which made saying
// something rather than nothing the cheapest way past the detector.
func TestScopeExpansionReportsItselfUnarmedOnAnIntentItCannotAct(t *testing.T) {
	t.Parallel()

	files := []precheck.File{{
		Path:           "internal/server/handler.go",
		AddedContent:   "package server\n",
		CurrentContent: "package server\n",
	}}

	for _, probe := range []struct {
		name        string
		intent      string
		wantUnarmed bool
	}{
		{name: "no intent", intent: "", wantUnarmed: true},
		{name: "vague intent", intent: "stuff", wantUnarmed: true},
		{name: "intent with no limit", intent: "Refresh the handler and tidy the imports.", wantUnarmed: true},
		{name: "runtime limit", intent: "Move the example without adding runtime behavior.", wantUnarmed: false},
		{name: "schema limit", intent: "Rename the column without changing the database schema.", wantUnarmed: false},
		{name: "only limit", intent: "Touch the README only.", wantUnarmed: false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			result := precheck.Scan(files, probe.intent)
			named := false
			for _, reason := range result.Unarmed {
				if strings.HasPrefix(reason, "scope-expansion") {
					named = true
				}
			}
			if named != probe.wantUnarmed {
				t.Fatalf("scope-expansion unarmed = %v, want %v; unarmed = %v", named, probe.wantUnarmed, result.Unarmed)
			}
		})
	}
}
