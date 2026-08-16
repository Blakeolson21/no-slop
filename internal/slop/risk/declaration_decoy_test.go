package risk_test

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

// The round-3 review defeated the positive declaration rule six times, in five
// languages, without touching the weakening itself. Each probe pairs a real
// cross-package authorization substitution with a second file that supplies the
// declaration evidence for free: a renamed parameter, a renamed dead function,
// or in the sharpest case a TODO comment nobody edited. All six scored
// "novelty: 0" and reached "verdict: pass" at exit 0.
//
// The shapes live here as unit probes and in internal/slop/adversarial as
// end-to-end corpus cases. Both matter: the corpus proves the exit code, these
// prove the axis, and an axis that goes back to 0 is the regression to catch
// first.

// decoyProbe is one reviewer probe reduced to the classifier's input.
type decoyProbe struct {
	name    string
	weaken  risk.FileChange
	decoy   risk.FileChange
	nothing string
}

func TestDeclarationDecoyCannotBuyTheMechanicalRoute(t *testing.T) {
	t.Parallel()

	probes := []decoyProbe{
		{
			name: "N1 javascript parameter renamed to the target name",
			weaken: risk.FileChange{
				Path:            "src/handler.js",
				Status:          risk.Modified,
				BaselineContent: "import { requireAdmin } from \"../lib/auth.js\";\n\nexport const guard = requireAdmin;\n",
				CurrentContent:  "import { allowAnyone } from \"../lib/auth.js\";\n\nexport const guard = allowAnyone;\n",
			},
			decoy: risk.FileChange{
				Path:            "src/util.js",
				Status:          risk.Modified,
				BaselineContent: "export function helper(x) {\n  return x + 1;\n}\n",
				CurrentContent:  "export function helper(allowAnyone) {\n  return allowAnyone + 1;\n}\n",
			},
		},
		{
			name: "N2 go dead fallback renamed into the target name",
			weaken: risk.FileChange{
				Path:            "internal/server/serve.go",
				Status:          risk.Modified,
				BaselineContent: "package server\n\n// Guard is the policy the request path consults.\nvar Guard = requireAdmin\n",
				CurrentContent:  "package server\n\n// Guard is the policy the request path consults.\nvar Guard = openAccess\n",
			},
			decoy: risk.FileChange{
				Path:            "internal/server/legacy.go",
				Status:          risk.Modified,
				BaselineContent: "package server\n\nfunc unusedFallback(role string) bool {\n\treturn true\n}\n",
				CurrentContent:  "package server\n\nfunc openAccess(role string) bool {\n\treturn true\n}\n",
			},
		},
		{
			name: "N3 python helper renamed into the target name",
			weaken: risk.FileChange{
				Path:            "handlers/api.py",
				Status:          risk.Modified,
				BaselineContent: "from policy.checks import require_admin\n\nGUARD = require_admin\n",
				CurrentContent:  "from policy.checks import allow_anyone\n\nGUARD = allow_anyone\n",
			},
			decoy: risk.FileChange{
				Path:            "tools/report.py",
				Status:          risk.Modified,
				BaselineContent: "def helper(value):\n    return value + 1\n",
				CurrentContent:  "def allow_anyone(value):\n    return value + 1\n",
			},
		},
		{
			name: "N4b javascript untouched TODO comment as the only evidence",
			weaken: risk.FileChange{
				Path:            "src/handler.js",
				Status:          risk.Modified,
				BaselineContent: "import { requireAdmin } from \"../lib/auth.js\";\n\nexport const guard = requireAdmin;\n",
				CurrentContent:  "import { allowAnyone } from \"../lib/auth.js\";\n\nexport const guard = allowAnyone;\n",
			},
			decoy: risk.FileChange{
				Path:            "tools/report.js",
				Status:          risk.Modified,
				BaselineContent: "// TODO: let allowAnyone be dropped once the migration lands.\nexport function helper(x) {\n  return x + 1;\n}\n",
				CurrentContent:  "// TODO: let allowAnyone be dropped once the migration lands.\nexport function assist(x) {\n  return x + 1;\n}\n",
			},
		},
		{
			name: "N6 typescript parameter renamed to the target name",
			weaken: risk.FileChange{
				Path:            "src/Guard.tsx",
				Status:          risk.Modified,
				BaselineContent: "import { requireAdmin } from \"./auth\";\n\nexport const activeGuard = requireAdmin;\n",
				CurrentContent:  "import { allowAnyone } from \"./auth\";\n\nexport const activeGuard = allowAnyone;\n",
			},
			decoy: risk.FileChange{
				Path:            "src/Panel.tsx",
				Status:          risk.Modified,
				BaselineContent: "export function renderPanel(x: number) {\n  return x + 1;\n}\n",
				CurrentContent:  "export function renderPanel(allowAnyone: number) {\n  return allowAnyone + 1;\n}\n",
			},
		},
		{
			name: "N8 ruby helper renamed into the target name",
			weaken: risk.FileChange{
				Path:            "app/web/router.rb",
				Status:          risk.Modified,
				BaselineContent: "require_relative \"../policy/checks\"\n\nGUARD = method(:require_admin)\n",
				CurrentContent:  "require_relative \"../policy/checks\"\n\nGUARD = method(:allow_anyone)\n",
			},
			decoy: risk.FileChange{
				Path:            "lib/report.rb",
				Status:          risk.Modified,
				BaselineContent: "def helper(value)\n  value + 1\nend\n",
				CurrentContent:  "def allow_anyone(value)\n  value + 1\nend\n",
			},
		},
	}

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			decision := classify(t, probe.weaken, probe.decoy)
			if decision.Novelty.Score == 0 {
				t.Fatalf("novelty = 0 (%s): a decoy declaration bought the mechanical route", decision.Novelty.Reason)
			}
			if decision.Tier == risk.TierLeakScanOnly {
				t.Fatalf("tier = %q, want a tier that reviews the substitution", decision.Tier)
			}
		})
	}
}

// TestCommentTextIsNeverADeclaration isolates the cheapest half of N4b. The
// declaration set was built from a raw token stream with no comment handling at
// all, so any prose containing a declaring keyword followed by a name declared
// that name.
func TestCommentTextIsNeverADeclaration(t *testing.T) {
	t.Parallel()

	for _, comment := range []struct {
		name string
		path string
		text string
	}{
		{name: "go line comment", path: "pkg/notes.go", text: "// let allowAnyone stay for now\n"},
		{name: "go block comment", path: "pkg/notes.go", text: "/* var allowAnyone is planned */\n"},
		{name: "python hash comment", path: "pkg/notes.py", text: "# def allow_anyone is planned\n"},
		{name: "ruby hash comment", path: "pkg/notes.rb", text: "# def allow_anyone is planned\n"},
		{name: "sql dash comment", path: "pkg/notes.sql", text: "-- let allow_anyone is planned\n"},
	} {
		t.Run(comment.name, func(t *testing.T) {
			t.Parallel()
			// The decoy file's only edit is renaming an unrelated helper. Its
			// comment names the target, and under the previous rule that alone
			// declared it.
			decoy := risk.FileChange{
				Path:            comment.path,
				Status:          risk.Modified,
				BaselineContent: comment.text + "func helper(value int) int { return value }\n",
				CurrentContent:  comment.text + "func assist(value int) int { return value }\n",
			}
			weaken := risk.FileChange{
				Path:            "pkg/use" + extensionOf(comment.path),
				Status:          risk.Modified,
				BaselineContent: "var guard = requireAdmin\n",
				CurrentContent:  "var guard = allowAnyone\n",
			}
			if decision := classify(t, weaken, decoy); decision.Novelty.Score == 0 {
				t.Fatalf("novelty = 0 (%s): a comment supplied the declaration", decision.Novelty.Reason)
			}
		})
	}
}

func extensionOf(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '.' {
			return path[index:]
		}
	}
	return ""
}

// TestALocalBindingCannotVouchForAnotherFile is the sharper decoy the reviewer
// did not run, found while implementing the transition rule. Requiring the
// change set to carry the exact old-to-new transition is not enough on its own:
// a parameter can be named anything, so `function helper(requireAdmin)`
// renamed to `function helper(allowAnyone)` carries precisely the transition a
// cross-file guard swap wants, and costs nothing to plant. A parameter is
// scoped to one function body and can never be what a reference in a different
// file resolves to, so it may not supply the evidence.
func TestALocalBindingCannotVouchForAnotherFile(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name  string
		decoy risk.FileChange
	}{
		{
			name: "parameter carrying the exact transition",
			decoy: risk.FileChange{
				Path:            "src/util.js",
				Status:          risk.Modified,
				BaselineContent: "export function helper(requireAdmin) {\n  return requireAdmin + 1;\n}\n",
				CurrentContent:  "export function helper(allowAnyone) {\n  return allowAnyone + 1;\n}\n",
			},
		},
		{
			name: "short variable carrying the exact transition",
			decoy: risk.FileChange{
				Path:            "internal/tool/report.go",
				Status:          risk.Modified,
				BaselineContent: "package tool\n\nfunc Run() int {\n\trequireAdmin := 1\n\treturn requireAdmin\n}\n",
				CurrentContent:  "package tool\n\nfunc Run() int {\n\tallowAnyone := 1\n\treturn allowAnyone\n}\n",
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			weaken := risk.FileChange{
				Path:            "src/handler" + extensionOf(probe.decoy.Path),
				Status:          risk.Modified,
				BaselineContent: "const guard = requireAdmin\n",
				CurrentContent:  "const guard = allowAnyone\n",
			}
			if decision := classify(t, weaken, probe.decoy); decision.Novelty.Score == 0 {
				t.Fatalf("novelty = 0 (%s): a function-local binding vouched for another file", decision.Novelty.Reason)
			}
		})
	}
}

// TestAFileMayFollowItsOwnLocalRename is the control for the rule above. Within
// one file a renamed parameter really is the same binding, and refusing that
// would make every local variable rename cost a review round.
func TestAFileMayFollowItsOwnLocalRename(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "src/util.js",
		Status:          risk.Modified,
		BaselineContent: "export function helper(count) {\n  return count + 1;\n}\n",
		CurrentContent:  "export function helper(total) {\n  return total + 1;\n}\n",
	})
	if decision.Novelty.Score != 0 {
		t.Fatalf("novelty = %d (%s), want 0 for a local parameter rename",
			decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestDeclarationEvidenceDoesNotCrossLanguages pins the family rule. A Go file
// declaring a name says nothing about what a JavaScript module's symbols
// resolve to, and letting one vouch for the other made the declaration set one
// flat namespace across every language in the diff.
func TestDeclarationEvidenceDoesNotCrossLanguages(t *testing.T) {
	t.Parallel()

	decision := classify(t,
		risk.FileChange{
			Path:            "src/handler.js",
			Status:          risk.Modified,
			BaselineContent: "import { requireAdmin } from \"../lib/auth.js\";\nexport const guard = requireAdmin;\n",
			CurrentContent:  "import { allowAnyone } from \"../lib/auth.js\";\nexport const guard = allowAnyone;\n",
		},
		risk.FileChange{
			Path:            "internal/tool/report.go",
			Status:          risk.Modified,
			BaselineContent: "package tool\n\nfunc requireAdmin(value int) int { return value }\n",
			CurrentContent:  "package tool\n\nfunc allowAnyone(value int) int { return value }\n",
		},
	)
	if decision.Novelty.Score == 0 {
		t.Fatalf("novelty = 0 (%s): a Go rename vouched for a JavaScript substitution", decision.Novelty.Reason)
	}
}

// TestRenameIsRefusedWhileTheOldNameStillAppears is the "uses were replaced"
// half. A change that retires a name and keeps using it somewhere else in the
// same diff has not renamed anything; it has chosen between two symbols.
func TestRenameIsRefusedWhileTheOldNameStillAppears(t *testing.T) {
	t.Parallel()

	decision := classify(t,
		risk.FileChange{
			Path:            "lib/name.js",
			Status:          risk.Modified,
			BaselineContent: "export function oldHelperName(value) {\n\treturn value + 1\n}\n",
			CurrentContent:  "export function newHelperName(value) {\n\treturn value + 1\n}\n",
		},
		risk.FileChange{
			Path:            "src/use.js",
			Status:          risk.Modified,
			BaselineContent: "const first = oldHelperName\nconst second = oldHelperName\n",
			CurrentContent:  "const first = newHelperName\nconst second = oldHelperName\n",
		},
	)
	if decision.Novelty.Score == 0 {
		t.Fatalf("novelty = 0 (%s): only one of the two uses was replaced", decision.Novelty.Reason)
	}
}

// TestAThrowawayFileCannotVouchForAnotherFilesSubstitution is the round-4
// probe, and it replaces the test that required a cross-file campaign to score
// novelty 0.
//
// The old test and this probe differ in exactly one respect: whether the file
// performing the transition is the one the use site's reference resolves to.
// `src/handler.js` imports `allowAnyone` from `lib/policy.js`, which the diff
// never touches, while `tools/dead.js` renames its own dead helper through the
// same pair and appears nowhere in `src/handler.js`. No rule that matches names
// rather than resolving references can separate them, so cross-file vouching is
// gone and the campaign pays a review round.
func TestAThrowawayFileCannotVouchForAnotherFilesSubstitution(t *testing.T) {
	t.Parallel()

	decision := classify(t,
		risk.FileChange{
			Path:            "src/handler.js",
			Status:          risk.Modified,
			BaselineContent: "import { requireAdmin } from '../lib/policy.js'\nexport const guard = requireAdmin\n",
			CurrentContent:  "import { allowAnyone } from '../lib/policy.js'\nexport const guard = allowAnyone\n",
		},
		risk.FileChange{
			Path:            "tools/dead.js",
			Status:          risk.Modified,
			BaselineContent: "function requireAdmin(value) {\n\treturn value\n}\nexport default requireAdmin\n",
			CurrentContent:  "function allowAnyone(value) {\n\treturn value\n}\nexport default allowAnyone\n",
		},
	)
	if decision.Novelty.Score == 0 {
		t.Fatalf("novelty = 0 (%s): a dead file's own rename paid for a guard swap it has no relationship to",
			decision.Novelty.Reason)
	}
}

// TestAHighRiskPathForfeitsTheMechanicalRoute covers the other half of the T2
// rule. On a path where the token stream is the runtime, a one-to-one token map
// does not mean the file still means what it meant.
func TestAHighRiskPathForfeitsTheMechanicalRoute(t *testing.T) {
	t.Parallel()

	decision := classify(t,
		risk.FileChange{
			Path:            ".claude/hooks/guard.js",
			Status:          risk.Modified,
			BaselineContent: "function requireAdmin(user) {\n\treturn user.admin\n}\nexport default requireAdmin\n",
			CurrentContent:  "function allowAnyone(user) {\n\treturn user.admin\n}\nexport default allowAnyone\n",
		},
	)
	if decision.Novelty.Score == 0 {
		t.Fatalf("novelty = 0 (%s): a self-contained rename on an instruction surface took the cheap route",
			decision.Novelty.Reason)
	}
}
