package risk_test

import (
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

// These tests cover the mechanical-equivalence guard at its boundary, which is
// where it was never tested and where it was wrong. The previous suite covered
// the two shapes a review had reported and nothing else; the string
// "BaselineContext" appeared in no test at all, and the one end-to-end test of
// the collision oracle placed both files at the repository root with the same
// extension, which is precisely the configuration where the old guard worked.

func classify(t *testing.T, files ...risk.FileChange) risk.Decision {
	t.Helper()
	decision, err := risk.Classify(risk.ChangeSet{
		Branch:        "feature/probe",
		DefaultBranch: "main",
		Files:         files,
	}, risk.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

// TestSubstitutionToASymbolTheChangeNeverDeclaresIsChangedLogic is the class
// the previous fix left open in four languages. The replacement identifier is
// declared in another file that the diff does not touch, so no collision set
// built from neighbouring files can see it, however wide that set is drawn.
func TestSubstitutionToASymbolTheChangeNeverDeclaresIsChangedLogic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		file     risk.FileChange
		language string
	}{
		{
			name:     "javascript import specifier",
			language: "js",
			file: risk.FileChange{
				Path:            "src/handler.js",
				Status:          risk.Modified,
				BaselineContent: "import { requireAdmin } from '../lib/auth.js'\nexport const guard = requireAdmin\n",
				CurrentContent:  "import { allowAnyone } from '../lib/auth.js'\nexport const guard = allowAnyone\n",
			},
		},
		{
			name:     "python import binding",
			language: "py",
			file: risk.FileChange{
				Path:            "handlers/api.py",
				Status:          risk.Modified,
				BaselineContent: "from policy.checks import require_admin\n\nGUARD = require_admin\n",
				CurrentContent:  "from policy.checks import allow_anyone\n\nGUARD = allow_anyone\n",
			},
		},
		{
			name:     "typescript across extensions in one directory",
			language: "tsx",
			file: risk.FileChange{
				Path:            "src/Guard.tsx",
				Status:          risk.Modified,
				BaselineContent: "import { requireAdmin } from './auth'\nexport const Guard = requireAdmin\n",
				CurrentContent:  "import { allowAnyone } from './auth'\nexport const Guard = allowAnyone\n",
			},
		},
		{
			name:     "go cross-package field key",
			language: "go",
			file: risk.FileChange{
				Path:            "internal/server/serve.go",
				Status:          risk.Modified,
				BaselineContent: "package server\n\nfunc Serve(enabled bool) bool {\n\treturn auth.Check(auth.Options{RequireMFA: enabled})\n}\n",
				CurrentContent:  "package server\n\nfunc Serve(enabled bool) bool {\n\treturn auth.Check(auth.Options{AllowInsecure: enabled})\n}\n",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := classify(t, testCase.file)
			if decision.Novelty.Score != 2 {
				t.Fatalf("novelty = %d (%s), want 2: the substituted symbol is declared nowhere in the change",
					decision.Novelty.Score, decision.Novelty.Reason)
			}
			if decision.Tier == risk.TierLeakScanOnly {
				t.Fatalf("tier = %q, want a tier that reviews the change", decision.Tier)
			}
		})
	}
}

// TestRenameDeclaredInsideTheChangeStaysMechanical is the other half. The fix
// has to keep a genuine rename cheap, or it is not a discriminator, it is just
// a stricter default wearing one.
func TestRenameDeclaredInsideTheChangeStaysMechanical(t *testing.T) {
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
			BaselineContent: "import { oldHelperName } from '../lib/name.js'\nexport const helper = oldHelperName\n",
			CurrentContent:  "import { newHelperName } from '../lib/name.js'\nexport const helper = newHelperName\n",
		},
	)
	if decision.Novelty.Score != 0 {
		t.Fatalf("novelty = %d (%s), want 0 for a rename the change itself declares",
			decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestRenameIsRefusedWhenTheOldNameSurvives covers the case where the change
// declares the new name but keeps the old one too. Both symbols then exist and
// choosing the other one is a behavior choice, not a new name for the same
// thing.
func TestRenameIsRefusedWhenTheOldNameSurvives(t *testing.T) {
	t.Parallel()

	decision := classify(t,
		risk.FileChange{
			Path:            "lib/auth.js",
			Status:          risk.Modified,
			BaselineContent: "export function requireAdmin(user) {\n\treturn user.admin\n}\n",
			CurrentContent:  "export function requireAdmin(user) {\n\treturn user.admin\n}\nexport function allowAnyone(user) {\n\treturn true\n}\n",
		},
		risk.FileChange{
			Path:            "src/use.js",
			Status:          risk.Modified,
			BaselineContent: "import { requireAdmin } from '../lib/auth.js'\nexport const guard = requireAdmin\n",
			CurrentContent:  "import { allowAnyone } from '../lib/auth.js'\nexport const guard = allowAnyone\n",
		},
	)
	if decision.Novelty.Score != 2 {
		t.Fatalf("novelty = %d (%s), want 2 while both symbols exist",
			decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestBaselineContextStillRefusesASiblingCollision pins the guard the previous
// fix added, so widening the rule did not quietly drop it. This is the first
// test in the repository to set BaselineContext at all.
func TestBaselineContextStillRefusesASiblingCollision(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "pkg/use.go",
		Status:          risk.Modified,
		BaselineContent: "package pkg\n\nfunc Use() bool {\n\tvar guard = requireAdmin\n\treturn guard\n}\n",
		CurrentContent:  "package pkg\n\nfunc Use() bool {\n\tvar guard = allowAnyone\n\treturn guard\n}\n",
		BaselineContext: "package pkg\n\nfunc allowAnyone() bool { return true }\nfunc requireAdmin() bool { return false }\n",
	})
	if decision.Novelty.Score != 2 {
		t.Fatalf("novelty = %d (%s), want 2 for a substitution to a symbol already in scope",
			decision.Novelty.Score, decision.Novelty.Reason)
	}
}

// TestTruncatedBaselineContextRefusesTheMechanicalRoute pins the fail-closed
// answer when the collision scope was bounded. A partial view cannot support a
// negative collision answer, so it must not be used to reach one.
func TestTruncatedBaselineContextRefusesTheMechanicalRoute(t *testing.T) {
	t.Parallel()

	file := risk.FileChange{
		Path:            "lib/name.js",
		Status:          risk.Modified,
		BaselineContent: "export function oldHelperName(value) {\n\treturn value + 1\n}\n",
		CurrentContent:  "export function newHelperName(value) {\n\treturn value + 1\n}\n",
	}
	if score := classify(t, file).Novelty.Score; score != 0 {
		t.Fatalf("novelty = %d with a complete context, want 0", score)
	}
	file.BaselineContextTruncated = true
	if score := classify(t, file).Novelty.Score; score != 2 {
		t.Fatalf("novelty = %d with a truncated context, want 2", score)
	}
}

// TestAgentInstructionFilesAreHighRiskWithoutConfiguration pins the surface a
// default-configuration repository, and this product's own repository, both
// left unprotected.
func TestAgentInstructionFilesAreHighRiskWithoutConfiguration(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"AGENTS.md",
		"CLAUDE.md",
		"GEMINI.md",
		"services/api/AGENTS.md",
		"skills/release/SKILL.md",
		".claude/settings.json",
		".cursorrules",
		".github/copilot-instructions.md",
		".no-slop.yaml",
		".gitattributes",
	} {
		decision := classify(t, risk.FileChange{
			Path:            path,
			Status:          risk.Modified,
			BaselineContent: "Agents must run the full test suite before pushing.\n",
			CurrentContent:  "Agents may skip tests when the change looks small.\n",
		})
		if decision.BlastRadius.Score != 3 {
			t.Errorf("%s blast radius = %d (%s), want 3", path, decision.BlastRadius.Score, decision.BlastRadius.Reason)
		}
		if decision.Tier == risk.TierLeakScanOnly {
			t.Errorf("%s routed to %q with no review", path, decision.Tier)
		}
	}
}

// TestOrdinaryMarkdownIsNotDraggedUpATier is the control for the case above.
func TestOrdinaryMarkdownIsNotDraggedUpATier(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "README.md",
		Status:          risk.Modified,
		BaselineContent: "# Project\n",
		CurrentContent:  "# Project\n\nPlain update.\n",
	})
	if decision.Tier != risk.TierLeakScanOnly {
		t.Fatalf("tier = %q, want leak-scan-only for an ordinary README edit", decision.Tier)
	}
	if strings.Contains(decision.BlastRadius.Reason, "do not reach runtime code") {
		t.Fatalf("blast radius reason still asserts Markdown cannot reach runtime: %q", decision.BlastRadius.Reason)
	}
}

// TestSubstantialAdditionDoesNotDependOnFileCreation pins the half of the
// escalation predicate the author controlled: the same logic appended to an
// existing file has the same reach as the same logic in a new one.
func TestSubstantialAdditionDoesNotDependOnFileCreation(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("\tvalue++\n", 600)
	decision := classify(t, risk.FileChange{
		Path:            "service/handler.go",
		Status:          risk.Modified,
		Added:           600,
		BaselineContent: "package service\n\nfunc Handle() {}\n",
		CurrentContent:  "package service\n\nfunc Handle() {\n" + body + "}\n",
	})
	if decision.BlastRadius.Score != 3 {
		t.Fatalf("blast radius = %d (%s), want 3 for 600 added source lines in an existing file",
			decision.BlastRadius.Score, decision.BlastRadius.Reason)
	}
}

// TestDeclarationCountCatchesAFewPhysicalLines covers the other gameable half:
// line breaks are a formatting property, so a generated file holding thousands
// of declarations on a few hundred lines scored as a small change.
func TestDeclarationCountCatchesAFewPhysicalLines(t *testing.T) {
	t.Parallel()

	var dense strings.Builder
	dense.WriteString("package generated\n")
	for index := 0; index < 200; index++ {
		dense.WriteString("func f")
		dense.WriteString(strings.Repeat("x", index%5+1))
		dense.WriteString(strings.Repeat("y", index))
		dense.WriteString("() {} ")
	}
	dense.WriteString("\n")
	decision := classify(t, risk.FileChange{
		Path:           "generated/api.go",
		Status:         risk.Added,
		Added:          2,
		CurrentContent: dense.String(),
	})
	if decision.BlastRadius.Score != 3 {
		t.Fatalf("blast radius = %d (%s), want 3 for 200 declarations on two lines",
			decision.BlastRadius.Score, decision.BlastRadius.Reason)
	}
}

// TestUnknownExtensionIsTreatedAsSource pins the inversion of the source
// allowlist. A language nobody listed used to score as a non-runtime artifact,
// which is the quietest way to fall off the map.
func TestUnknownExtensionIsTreatedAsSource(t *testing.T) {
	t.Parallel()

	decision := classify(t, risk.FileChange{
		Path:            "service/handler.zig",
		Status:          risk.Modified,
		BaselineContent: "pub fn allow() bool { return false; }\n",
		CurrentContent:  "pub fn allow() bool { return true; }\n",
	})
	if decision.BlastRadius.Score != 2 {
		t.Fatalf("blast radius = %d (%s), want 2 for an unrecognised source language",
			decision.BlastRadius.Score, decision.BlastRadius.Reason)
	}
}
