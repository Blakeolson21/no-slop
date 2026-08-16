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

// TestExtractingGuardsIntoANewFileIsAMoveNotARemoval pins the change-set scope.
// The detector counted guard clauses per file, so extracting validation out of
// handler.go into a new validate.go in the same commit dropped handler.go's
// count and reported a removed guard, blocking at every tier with no exemption
// path, while the change set held exactly the guards it started with.
func TestExtractingGuardsIntoANewFileIsAMoveNotARemoval(t *testing.T) {
	t.Parallel()

	const guards = "\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\tif token == \"\" {\n\t\treturn errBadToken\n\t}\n"
	result := precheck.Scan([]precheck.File{
		{
			Path:            "internal/svc/handler.go",
			BaselineContent: "package svc\n\nfunc Handle(user, token string) error {\n" + guards + "\treturn nil\n}\n",
			CurrentContent:  "package svc\n\nfunc Handle(user, token string) error {\n\treturn validate(user, token)\n}\n",
		},
		{
			Path:            "internal/svc/validate.go",
			BaselineContent: "",
			AddedContent:    "package svc\n\nfunc validate(user, token string) error {\n" + guards + "\treturn nil\n}\n",
			CurrentContent:  "package svc\n\nfunc validate(user, token string) error {\n" + guards + "\treturn nil\n}\n",
		},
	}, "")
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") {
			t.Fatalf("relocating guards into a new file reported a removal: %+v", finding)
		}
	}
}

// TestUnrelatedGuardPaddingDoesNotExcuseARemoval closes the hole that
// aggregating by count opened. The compensating clauses are written by the same
// change being judged, so any rule that only counts them is satisfied by
// padding: three unrelated `if err != nil { return err }` bodies added in the
// same commit brought the change-set total back to level and suppressed the
// blocking finding for a file that had just lost three authorization guards.
func TestUnrelatedGuardPaddingDoesNotExcuseARemoval(t *testing.T) {
	t.Parallel()

	padding := "package util\n\nfunc A(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\n" +
		"func B(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\n" +
		"func C(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	result := precheck.Scan([]precheck.File{
		{
			Path: "internal/auth/policy.go",
			BaselineContent: "package auth\n\nfunc Authorize(role, token, scope string) error {\n" +
				"\tif role != \"admin\" {\n\t\treturn errForbidden\n\t}\n" +
				"\tif token == \"\" {\n\t\treturn errNoToken\n\t}\n" +
				"\tif scope != \"write\" {\n\t\treturn errNoScope\n\t}\n" +
				"\treturn nil\n}\n",
			CurrentContent: "package auth\n\nfunc Authorize(role, token, scope string) error {\n\treturn nil\n}\n",
		},
		{
			Path:            "internal/util/pad.go",
			BaselineContent: "",
			AddedContent:    padding,
			CurrentContent:  padding,
		},
	}, "")
	found := false
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") && finding.Path == "internal/auth/policy.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("guard-shaped padding excused an authorization deletion: %+v", result.Findings)
	}
}

// TestSameFileGuardPaddingDoesNotExcuseARemoval closes the hole the identity
// pool left open by sitting behind a per-file count. Folding the padding into
// the file that lost the guards kept its guard total level, so the count
// trigger returned early and identity matching was never reached: three deleted
// authorization guards and three added `if err != nil` helpers produced no
// finding at all.
func TestSameFileGuardPaddingDoesNotExcuseARemoval(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{{
		Path: "internal/auth/policy.go",
		BaselineContent: "package auth\n\nfunc Authorize(role, token, scope string) error {\n" +
			"\tif role != \"admin\" {\n\t\treturn errForbidden\n\t}\n" +
			"\tif token == \"\" {\n\t\treturn errNoToken\n\t}\n" +
			"\tif scope != \"write\" {\n\t\treturn errNoScope\n\t}\n" +
			"\treturn nil\n}\n",
		CurrentContent: "package auth\n\nfunc Authorize(role, token, scope string) error {\n\treturn nil\n}\n\n" +
			"func A(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\n" +
			"func B(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\n" +
			"func C(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
	}}, "")
	found := false
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") && finding.Path == "internal/auth/policy.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("padding inside the shrinking file excused an authorization deletion: %+v", result.Findings)
	}
}

// TestGuardsKeptAsInertTextAreStillRemoved closes the cheapest bypass this
// detector had. Signatures were collected from raw lines, so parking the
// deleted clauses in a raw string literal or a block comment left them matching
// at head: the removal netted to zero and no finding was produced at all. That
// costs the author no compilable code, unlike every padding shape already
// refused.
func TestGuardsKeptAsInertTextAreStillRemoved(t *testing.T) {
	t.Parallel()

	const guards = "\tif role != \"admin\" {\n\t\treturn errForbidden\n\t}\n" +
		"\tif token == \"\" {\n\t\treturn errNoToken\n\t}\n"
	baseline := "package auth\n\nfunc Authorize(role, token string) error {\n" + guards + "\treturn nil\n}\n"
	gutted := "package auth\n\nfunc Authorize(role, token string) error {\n\treturn nil\n}\n\n"

	for _, probe := range []struct {
		name    string
		current string
	}{
		{"raw string literal", gutted + "const legacy = `\n" + guards + "`\n"},
		{"block comment", gutted + "/*\n" + guards + "*/\n"},
	} {
		result := precheck.Scan([]precheck.File{{
			Path:            "internal/auth/policy.go",
			BaselineContent: baseline,
			CurrentContent:  probe.current,
		}}, "")
		found := false
		for _, finding := range result.Findings {
			if strings.Contains(finding.Description, "refusing checks dropped") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: keeping the guards as inert text excused their removal: %+v", probe.name, result.Findings)
		}
	}
}

// TestCrossLanguageRenameStripsEachRevisionWithItsOwnSpec pins that the baseline
// is read as the language the BASE revision spelled it. Stripping is per file
// kind, so scanning a ported handler.py with the head's Go spec left its
// docstring unblanked; the guard-shaped prose inside counted as present at
// baseline and absent at head, and a port that deleted no check at all drew a
// blocking finding with no exemption path.
func TestCrossLanguageRenameStripsEachRevisionWithItsOwnSpec(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{{
		Path:         "internal/svc/handler.go",
		BaselinePath: "internal/svc/handler.py",
		BaselineContent: "def handle(token):\n" +
			"    \"\"\"Legacy notes, kept as prose rather than code.\n" +
			"    if token == \"\":\n" +
			"        return None\n" +
			"    \"\"\"\n" +
			"    return None\n",
		CurrentContent: "package svc\n\nfunc Handle(token string) error {\n\treturn nil\n}\n",
	}}, "")
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") {
			t.Fatalf("prose inside a baseline docstring was read as a deleted guard: %+v", finding)
		}
	}
}

// TestRemovedGuardFindingLocatesTheClauseInTheBaseRevision pins the operator's
// only route back to the clause. The description carries a digest rather than
// the source, so the coordinate is the whole locator, and it is a BASE-revision
// coordinate: the head file does not have the guard at that line, because the
// change deleted it. Rendering it as a bare path:line read as a head location.
func TestRemovedGuardFindingLocatesTheClauseInTheBaseRevision(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{{
		Path:         "internal/svc/handler.go",
		BaselinePath: "internal/svc/legacy_handler.go",
		BaselineContent: "package svc\n\nfunc Handle(user string) error {\n" +
			"\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\treturn nil\n}\n",
		CurrentContent: "package svc\n\nfunc Handle(user string) error {\n\treturn nil\n}\n",
	}}, "")
	found := false
	for _, finding := range result.Findings {
		if !strings.Contains(finding.Description, "refusing checks dropped") {
			continue
		}
		found = true
		if finding.Path != "internal/svc/legacy_handler.go" {
			t.Errorf("finding path = %q, want the base-revision spelling", finding.Path)
		}
		if finding.Line != 4 {
			t.Errorf("finding line = %d, want the baseline line that carried the guard", finding.Line)
		}
		if !strings.Contains(finding.Description, "BASE-revision") {
			t.Errorf("the finding does not say which revision its coordinate belongs to: %q", finding.Description)
		}
	}
	if !found {
		t.Fatalf("deleting the guard produced no finding: %+v", result.Findings)
	}
}

// TestRemovedGuardFindingDoesNotReproduceBaselineSource keeps deleted source out
// of stdout and the on-disk provenance record. The leak scan reads head content
// only, so a credential that exists solely at the baseline is exactly what
// deleting a hardcoded secret check removes: never scanned, never redacted.
func TestRemovedGuardFindingDoesNotReproduceBaselineSource(t *testing.T) {
	t.Parallel()

	const secret = "sk-live-51H8fAKEfAKEfAKEfAKE"
	result := precheck.Scan([]precheck.File{{
		Path:            "internal/auth/policy.go",
		BaselineContent: "package auth\n\nfunc Authorize(apiKey string) error {\n\tif apiKey != \"" + secret + "\" {\n\t\treturn errUnauthorized\n\t}\n\treturn nil\n}\n",
		CurrentContent:  "package auth\n\nfunc Authorize(apiKey string) error {\n\treturn nil\n}\n",
	}}, "")
	reported := false
	for _, finding := range result.Findings {
		if !strings.Contains(finding.Description, "refusing checks dropped") {
			continue
		}
		reported = true
		if strings.Contains(finding.Description, secret) {
			t.Fatalf("the finding reproduced a baseline-only credential: %q", finding.Description)
		}
		if finding.Line == 0 {
			t.Errorf("the finding carries no line, so the digest is the only way to locate the clause: %+v", finding)
		}
	}
	if !reported {
		t.Fatalf("deleting the guard produced no finding: %+v", result.Findings)
	}
}

// TestRelocatingAGuardWithinOneFileStaysSilent is the false-positive control for
// the test above. Moving a guard into a helper in the same file keeps the clause
// in the change set, so identity matching cancels it and nothing reports.
func TestRelocatingAGuardWithinOneFileStaysSilent(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{{
		Path: "internal/svc/handler.go",
		BaselineContent: "package svc\n\nfunc Handle(user string) error {\n" +
			"\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\treturn nil\n}\n",
		CurrentContent: "package svc\n\nfunc Handle(user string) error {\n\treturn check(user)\n}\n\n" +
			"func check(user string) error {\n\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\treturn nil\n}\n",
	}}, "")
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") {
			t.Fatalf("relocating a guard inside one file reported a removal: %+v", finding)
		}
	}
}

// TestExtractingGuardsAndDroppingOneStillReports is the control for the test
// above. Counting across the change set must not become a licence to delete a
// guard while moving its neighbours, so a change set that nets negative still
// blocks and still names the file that lost them.
func TestExtractingGuardsAndDroppingOneStillReports(t *testing.T) {
	t.Parallel()

	result := precheck.Scan([]precheck.File{
		{
			Path:            "internal/svc/handler.go",
			BaselineContent: "package svc\n\nfunc Handle(user, token string) error {\n\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\tif token == \"\" {\n\t\treturn errBadToken\n\t}\n\treturn nil\n}\n",
			CurrentContent:  "package svc\n\nfunc Handle(user, token string) error {\n\treturn validate(user, token)\n}\n",
		},
		{
			Path:            "internal/svc/validate.go",
			BaselineContent: "",
			AddedContent:    "package svc\n\nfunc validate(user, token string) error {\n\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\treturn nil\n}\n",
			CurrentContent:  "package svc\n\nfunc validate(user, token string) error {\n\tif user == \"\" {\n\t\treturn errBadUser\n\t}\n\treturn nil\n}\n",
		},
	}, "")
	found := false
	for _, finding := range result.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") && finding.Path == "internal/svc/handler.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a change set that net-deleted a guard reported nothing: %+v", result.Findings)
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
