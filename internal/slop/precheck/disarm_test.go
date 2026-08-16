package precheck_test

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/precheck"
)

// TestCommentCannotDisarmTheFailOpenDetector replaces an acquittal case that
// pinned the opposite. A comment claiming the permissive branch is deliberate
// is free to write and is verified against nothing, so honoring it made the
// detector disarmable by the party it was checking.
func TestCommentCannotDisarmTheFailOpenDetector(t *testing.T) {
	t.Parallel()

	for _, defence := range []string{
		"// Optional configuration is fail-open by policy.",
		"// This is the documented default.",
		"// Intentional fail-open, see the design note.",
	} {
		result := precheck.Scan([]precheck.File{{
			Path:           "optional.go",
			AddedContent:   "\n" + defence + "\nreturn nil, nil\n",
			CurrentContent: "if errors.Is(err, os.ErrNotExist) {\n" + defence + "\nreturn nil, nil\n}\n",
		}}, "")
		if !hasLens(result.Findings, "fail-open-default") {
			t.Errorf("comment %q suppressed the fail-open finding: %+v", defence, result.Findings)
		}
	}
}

// TestPermissiveReturnIsReadFromItsValues pins the shapes an exact-string
// whitelist missed. `return true, nil` grants exactly what `return true`
// grants; it was invisible because it was spelled for a function that also
// returns an error.
func TestPermissiveReturnIsReadFromItsValues(t *testing.T) {
	t.Parallel()

	permissive := []string{
		"return true",
		"return true, nil",
		"return nil, nil",
		"return true;",
		"return allowAll(user)",
	}
	for _, statement := range permissive {
		result := precheck.Scan([]precheck.File{{
			Path:           "guard.go",
			AddedContent:   "\n" + statement + "\n",
			CurrentContent: "if err != nil {\n" + statement + "\n}\n",
		}}, "")
		if !hasLens(result.Findings, "fail-open-default") {
			t.Errorf("permissive return %q produced no finding: %+v", statement, result.Findings)
		}
	}

	restrictive := []string{
		"return false, nil",
		"return nil, err",
		"return deny(user)",
	}
	for _, statement := range restrictive {
		result := precheck.Scan([]precheck.File{{
			Path:           "guard.go",
			AddedContent:   "\n" + statement + "\n",
			CurrentContent: "if err != nil {\n" + statement + "\n}\n",
		}}, "")
		if hasLens(result.Findings, "fail-open-default") {
			t.Errorf("restrictive return %q was reported as fail-open: %+v", statement, result.Findings)
		}
	}
}

// TestWorkaroundDetectionSurvivesRewording pins the phrasing independence the
// corpus-seeded literals lacked: "We work around the broken parser" fired and
// "Workaround for the broken parser" did not, for the same code.
func TestWorkaroundDetectionSurvivesRewording(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{
		"// We work around the broken parser here.",
		"// Workaround for the broken parser.",
		"// Working around the broken parser.",
		"// Temporary: the parser is broken.",
		"// For now the upstream check is flaky.",
	} {
		result := precheck.Scan([]precheck.File{{
			Path:           "client.go",
			AddedContent:   "\n" + comment + "\nreturn true\n",
			CurrentContent: comment + "\nreturn true\n",
		}}, "")
		if !hasLens(result.Findings, "comment-defended-workaround") {
			t.Errorf("comment %q produced no workaround finding: %+v", comment, result.Findings)
		}
	}
}

// TestFollowupDetectionSurvivesRewording is the same property for the
// asserted-followup lens.
func TestFollowupDetectionSurvivesRewording(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{
		"// Filed with the platform team.",
		"// Tracking this separately.",
		"// Will be done in the next pass.",
		"// Signed off by the reviewer.",
	} {
		result := precheck.Scan([]precheck.File{{
			Path:           "client.go",
			AddedContent:   "\n" + comment + "\n",
			CurrentContent: comment + "\nfunc run() {}\n",
		}}, "")
		if !hasLens(result.Findings, "asserted-followup-without-artifact") {
			t.Errorf("comment %q produced no follow-up finding: %+v", comment, result.Findings)
		}
	}
	withReference := "// Filed as https://example.com/issues/12."
	result := precheck.Scan([]precheck.File{{
		Path:           "client.go",
		AddedContent:   "\n" + withReference + "\n",
		CurrentContent: withReference + "\nfunc run() {}\n",
	}}, "")
	if hasLens(result.Findings, "asserted-followup-without-artifact") {
		t.Errorf("a durable reference should acquit: %+v", result.Findings)
	}
}

// TestDeletedGuardIsDetected covers the half of the diff every other detector
// ignored. Removing a check adds no lines, so a detector that reads added lines
// only reports nothing at all for the defect delivered by deletion.
func TestDeletedGuardIsDetected(t *testing.T) {
	t.Parallel()

	baseline := `package auth

func Allow(user User) (bool, error) {
	if user.Token == "" {
		return false, errors.New("no token")
	}
	if !user.Verified {
		return false, errors.New("unverified")
	}
	return true, nil
}
`
	current := `package auth

func Allow(user User) (bool, error) {
	if user.Token == "" {
		return false, errors.New("no token")
	}
	return true, nil
}
`
	result := precheck.Scan([]precheck.File{{
		Path:            "auth/policy.go",
		AddedContent:    "",
		BaselineContent: baseline,
		CurrentContent:  current,
	}}, "")
	if !hasLens(result.Findings, "fail-open-default") {
		t.Fatalf("deleting a guard produced no finding: %+v", result.Findings)
	}

	renamed := strings.ReplaceAll(baseline, "user", "caller")
	unchangedCount := precheck.Scan([]precheck.File{{
		Path:            "auth/policy.go",
		BaselineContent: baseline,
		CurrentContent:  renamed,
	}}, "")
	for _, finding := range unchangedCount.Findings {
		if strings.Contains(finding.Description, "refusing checks dropped") {
			t.Fatalf("renaming inside a guard was reported as removing one: %+v", finding)
		}
	}
}

// TestScanReportsDetectorsItCouldNotArm keeps the pre-check from claiming
// coverage it did not have. Scope expansion is only meaningful against a stated
// scope, so with no intent it must say it did not run rather than contribute a
// silent zero.
func TestScanReportsDetectorsItCouldNotArm(t *testing.T) {
	t.Parallel()

	withoutIntent := precheck.Scan([]precheck.File{{Path: "main.go", CurrentContent: "package main\n"}}, "")
	if len(withoutIntent.Unarmed) != 1 || !strings.Contains(withoutIntent.Unarmed[0], "scope-expansion") {
		t.Fatalf("unarmed = %+v, want the scope-expansion detector named", withoutIntent.Unarmed)
	}
	withIntent := precheck.Scan([]precheck.File{{Path: "main.go", CurrentContent: "package main\n"}}, "Touch main only.")
	if len(withIntent.Unarmed) != 0 {
		t.Fatalf("unarmed = %+v, want none once an intent is stated", withIntent.Unarmed)
	}
}

func hasLens(findings []precheck.Finding, lens string) bool {
	for _, finding := range findings {
		if finding.Lens == lens {
			return true
		}
	}
	return false
}
