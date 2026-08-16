package risk_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

// The round-5 review found the mechanical route open from the opposite side of
// every guard the round-4 fix installed. Those guards all live inside the loop
// over the substitution map, so a change that produces NO substitutions walks
// past all of them: mechanicallyEquivalent fell through to "the content
// differs", which is true of every change there has ever been, and the run
// printed "source token stream contains only consistent identifier
// substitutions" over a change containing zero substitutions.
//
// Both probes below are the reviewer's, reduced to the classifier's input. Each
// is one file and one line, with an honest remote and a default config, and
// each defeats the product's headline invariant in a language whose semantics
// are whitespace.

// TestMovedPythonReturnIsNotAMechanicalSubstitution is reviewer probe U2a. The
// loop stops requiring every check to pass and returns True after the first one
// does. The token streams are byte-identical because the tokenizer skipped
// whitespace, so the substitution map came back empty and the change scored
// novelty 0 at tier leak-scan-only, verdict pass, exit 0.
func TestMovedPythonReturnIsNotAMechanicalSubstitution(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "app/gatekeeper.py",
		Status:          risk.Modified,
		BaselineContent: "def authorize(user, checks):\n    for check in checks:\n        if not check(user):\n            return False\n    return True\n",
		CurrentContent:  "def authorize(user, checks):\n    for check in checks:\n        if not check(user):\n            return False\n        return True\n",
	})
	if decision.Novelty.Score < 2 {
		t.Fatalf("novelty = %d (%s), want at least 2: moving a return out of a loop is changed logic", decision.Novelty.Score, decision.Novelty.Reason)
	}
	assertNoSubstitutionClaim(t, decision)
}

// TestShellWordSplitIsNotAMechanicalSubstitution is reviewer probe U2b. The
// head version deletes the build root instead of the artifacts directory under
// it. `$BUILD/artifacts` and `$BUILD /artifacts` tokenize identically once
// whitespace is skipped, so this reached verdict pass at exit 0 as well, in a
// change that has nothing to do with authorization.
func TestShellWordSplitIsNotAMechanicalSubstitution(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "scripts/release.sh",
		Status:          risk.Modified,
		BaselineContent: "BUILD=/var/lib/app/build\nrm -rf $BUILD/artifacts\n",
		CurrentContent:  "BUILD=/var/lib/app/build\nrm -rf $BUILD /artifacts\n",
	})
	if decision.Novelty.Score < 2 {
		t.Fatalf("novelty = %d (%s), want at least 2: a shell word split changes what the command deletes", decision.Novelty.Score, decision.Novelty.Reason)
	}
	assertNoSubstitutionClaim(t, decision)
}

// TestTokenIdenticalEditInABraceLanguageIsStillChangedLogic pins the root
// rather than the two languages the reviewer used. The fallthrough was not
// language-specific: any file whose token stream did not change took the
// mechanical route, and reformatting a C-family file is the cheapest way there.
// The reason string matters as much as the score, because a reviewer reading
// "only consistent identifier substitutions" over an empty map has been told
// something that is not true.
func TestTokenIdenticalEditInABraceLanguageIsStillChangedLogic(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "src/handler.js",
		Status:          risk.Modified,
		BaselineContent: "export function guard(user) {\n  return user.role === 'admin' && user.mfa\n}\n",
		CurrentContent:  "export function guard(user) {\n  return user.role === 'admin' && user.mfa }\n",
	})
	if decision.Novelty.Score < 2 {
		t.Fatalf("novelty = %d (%s), want at least 2: an edit with no token difference carries no rename evidence", decision.Novelty.Score, decision.Novelty.Reason)
	}
	assertNoSubstitutionClaim(t, decision)
}

// TestRenameCarryingAnIndentationMoveIsNotMechanical is the half that requiring
// a real token change does not reach. Here the substitution map is genuinely
// non-empty and the rename is the file's own declared transition, so every
// round-4 guard is satisfied and the loop runs to completion. The change also
// moves a return one level in, which the token stream cannot see while
// whitespace is skipped. In Python that is the whole edit that matters.
func TestRenameCarryingAnIndentationMoveIsNotMechanical(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "app/gatekeeper.py",
		Status:          risk.Modified,
		BaselineContent: "def verify_all(user, checks):\n    for check in checks:\n        if not check(user):\n            return False\n    return True\n",
		CurrentContent:  "def authorize_all(user, checks):\n    for check in checks:\n        if not check(user):\n            return False\n        return True\n",
	})
	if decision.Novelty.Score < 2 {
		t.Fatalf("novelty = %d (%s), want at least 2: the rename is real and the indentation move is a logic change riding along with it", decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestShellRenameCarryingAWordSplitIsNotMechanical is the same shape in the
// other whitespace-significant language the reviewer used, where the semantic
// whitespace is inside a line rather than at its start.
func TestShellRenameCarryingAWordSplitIsNotMechanical(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "scripts/release.sh",
		Status:          risk.Modified,
		BaselineContent: "stage_root=/var/lib/app/build\nrm -rf $stage_root/artifacts\n",
		CurrentContent:  "build_root=/var/lib/app/build\nrm -rf $build_root /artifacts\n",
	})
	if decision.Novelty.Score < 2 {
		t.Fatalf("novelty = %d (%s), want at least 2: the rename is real and the word split changes what is deleted", decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestReformattingABraceLanguageRenameStaysMechanical is the boundary on the
// other side, and it is why line structure is a per-language property rather
// than a blanket rule. In a language whose blocks are delimited by braces,
// where the newlines fall is not a statement about behavior, so a genuine
// same-file rename that also reflows the body must keep the cheap route it
// already had. Making every language whitespace-significant would have bought
// the U2 fix by charging a review round for every gofmt run.
func TestReformattingABraceLanguageRenameStaysMechanical(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "src/util.js",
		Status:          risk.Modified,
		BaselineContent: "export function computeTotal(items) {\n  return items.length\n}\n",
		CurrentContent:  "export function computeSum(items) { return items.length }\n",
	})
	if decision.Novelty.Score != 0 {
		t.Fatalf("novelty = %d (%s), want 0: a same-file rename that reflows a brace-delimited body is still a rename", decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// assertNoSubstitutionClaim refuses the reason string the fallthrough printed.
// A run that names substitutions it did not find is unauditable in exactly the
// way a passing verdict on a defeated base is: the output reads like evidence.
func assertNoSubstitutionClaim(t *testing.T, decision risk.Decision) {
	t.Helper()
	if strings.Contains(decision.Novelty.Reason, "consistent identifier substitutions") {
		t.Fatalf("novelty reason claims consistent identifier substitutions on a change that has none: %q", decision.Novelty.Reason)
	}
}
