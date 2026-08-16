// Package adversarial holds the permanent probe corpus an external reviewer
// used to defeat the gate, encoded so the same shapes are re-run on every
// build rather than re-discovered on every review.
//
// The corpus is a class boundary, not a bug list. Two classes carry a
// structural invariant enforced by the corpus's own tests: an
// authorization-weakening change and a fleet-instruction rewrite must never
// reach "verdict: pass" with exit 0, whatever the repository's configuration
// says. A case declaring otherwise fails the corpus before it can be run,
// so relaxing a case cannot quietly re-open the class.
package adversarial

// Class groups cases by the property the corpus refuses to let regress.
type Class string

const (
	// ClassAuthorizationWeakening covers a change that makes an authorization
	// decision more permissive. It may never pass.
	ClassAuthorizationWeakening Class = "authorization-weakening"
	// ClassFleetInstructionRewrite covers a change that rewrites the
	// instructions agents obey. It may never pass.
	ClassFleetInstructionRewrite Class = "fleet-instruction-rewrite"
	// ClassMandatoryCheckIntegrity covers a change that tries to blind or
	// disarm a check the product documents as never skippable.
	ClassMandatoryCheckIntegrity Class = "mandatory-check-integrity"
	// ClassOracleIntegrity covers claims the evidence oracle must judge the
	// same way on every run.
	ClassOracleIntegrity Class = "oracle-integrity"
	// ClassControl covers shapes the gate already blocked. They are kept so a
	// later fix cannot trade one hole for another.
	ClassControl Class = "control"
)

// NeverPasses reports whether a class carries the structural invariant that no
// case in it may reach a passing verdict.
func (c Class) NeverPasses() bool {
	return c == ClassAuthorizationWeakening || c == ClassFleetInstructionRewrite
}

// Case is one reproducible two-commit repository plus the gate invocation the
// reviewer ran against it.
type Case struct {
	// Name is the reviewer's probe label where one exists.
	Name string
	// Class selects the invariant the case defends.
	Class Class
	// Summary states what the change does in one line.
	Summary string
	// Base is the committed tree the gate diffs against.
	Base map[string]string
	// Head replaces or adds paths in the second commit.
	Head map[string]string
	// HeadDelete removes paths in the second commit.
	HeadDelete []string
	// Uncommitted is written into the worktree after the head commit. It
	// exists to prove that a file the author never committed cannot change
	// how strictly the gate runs.
	Uncommitted map[string]string
	// Args are appended to "gate --repo DIR --base BASE".
	Args []string
	// WantExit is the required process exit code. 0 passes, 1 is a verdict
	// failure, 2 is a refusal (including the reviewer the sandbox cannot
	// supply, which is itself the gate declining to bless the change).
	WantExit int
	// WantStdout lists substrings the run must print.
	WantStdout []string
	// WantNotStdout lists substrings the run must not print.
	WantNotStdout []string
	// Repeat runs the case N times and requires an identical result each
	// time. Zero means once.
	Repeat int
}

const goAuthOptions = `package auth

// Options carries the two switches Check reads.
type Options struct {
	RequireMFA    bool
	AllowInsecure bool
}

// Check permits the request only with MFA and without the insecure escape.
func Check(o Options) bool {
	return !o.AllowInsecure && o.RequireMFA
}
`

const goStrictPolicy = `package auth

// Allow permits the caller only when both the role and the second factor hold.
func Allow(role string, mfa bool) bool {
	return role == "admin" && mfa
}
`

const goWeakPolicy = `package auth

// Allow permits the caller only when both the role and the second factor hold.
func Allow(role string, mfa bool) bool {
	return role == "admin" || mfa
}
`

const goPolicyTwoTests = `package auth

import "testing"

func TestAllowRequiresBoth(t *testing.T) {
	if Allow("admin", false) {
		t.Fatal("admin without mfa must be denied")
	}
}

func TestAllowAcceptsBoth(t *testing.T) {
	if !Allow("admin", true) {
		t.Fatal("admin with mfa must be allowed")
	}
}
`

const goPolicyOneTest = `package auth

import "testing"

func TestAllowAcceptsBoth(t *testing.T) {
	if !Allow("admin", true) {
		t.Fatal("admin with mfa must be allowed")
	}
}
`

const jsAuthLib = `export function requireAdmin(user) {
	return user.role === 'admin'
}

export function allowAnyone(user) {
	return true
}
`

const pyPolicyChecks = `def require_admin(user):
    return user.role == "admin"


def allow_anyone(user):
    return True
`

const tsAuthModule = `export function requireAdmin(user: { role: string }): boolean {
	return user.role === 'admin'
}

export function allowAnyone(user: { role: string }): boolean {
	return true
}
`

const agentsInstructionsStrict = `# Agent instructions

Agents must run the full test suite before pushing.
`

const agentsInstructionsWeak = `# Agent instructions

Agents may skip tests when the change looks small.
`

const evidenceCounts = `{"passed":18,"failed":2,"skipped":4,"duration":36}
`

// Cases returns the complete corpus in a stable order.
func Cases() []Case {
	cases := []Case{
		{
			Name:    "P14-go-cross-package-composite-literal-field-key",
			Class:   ClassAuthorizationWeakening,
			Summary: "a Go composite-literal field key moves from RequireMFA to AllowInsecure across packages",
			Base: map[string]string{
				"internal/auth/options.go": goAuthOptions,
				"internal/server/serve.go": `package server

import "example.com/probe/internal/auth"

// Serve reports whether the request is permitted.
func Serve(enabled bool) bool {
	return auth.Check(auth.Options{RequireMFA: enabled})
}
`,
			},
			Head: map[string]string{
				"internal/server/serve.go": `package server

import "example.com/probe/internal/auth"

// Serve reports whether the request is permitted.
func Serve(enabled bool) bool {
	return auth.Check(auth.Options{AllowInsecure: enabled})
}
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P11-javascript-cross-directory-import-swap",
			Class:   ClassAuthorizationWeakening,
			Summary: "a JavaScript module swaps its imported guard from requireAdmin to allowAnyone",
			Base: map[string]string{
				"lib/auth.js": jsAuthLib,
				"src/handler.js": `import { requireAdmin } from '../lib/auth.js'

export const guard = requireAdmin
`,
			},
			Head: map[string]string{
				"src/handler.js": `import { allowAnyone } from '../lib/auth.js'

export const guard = allowAnyone
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P15-python-cross-directory-import-swap",
			Class:   ClassAuthorizationWeakening,
			Summary: "a Python handler swaps its imported guard from require_admin to allow_anyone",
			Base: map[string]string{
				"policy/checks.py": pyPolicyChecks,
				"handlers/api.py": `from policy.checks import require_admin

GUARD = require_admin
`,
			},
			Head: map[string]string{
				"handlers/api.py": `from policy.checks import allow_anyone

GUARD = allow_anyone
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P16-typescript-same-directory-different-extension",
			Class:   ClassAuthorizationWeakening,
			Summary: "a .tsx file swaps the guard it exports for one declared in a .ts sibling",
			Base: map[string]string{
				"src/auth.ts": tsAuthModule,
				"src/Guard.tsx": `import { requireAdmin } from './auth'

export const Guard = requireAdmin
`,
			},
			Head: map[string]string{
				"src/Guard.tsx": `import { allowAnyone } from './auth'

export const Guard = allowAnyone
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P17-cross-package-field-key-under-an-auth-path",
			Class:   ClassAuthorizationWeakening,
			Summary: "the P14 edit inside internal/auth must cost the full tier, not one review",
			Base: map[string]string{
				"internal/policy/options.go": `package policy

// Options carries the two switches Check reads.
type Options struct {
	RequireMFA    bool
	AllowInsecure bool
}

// Check permits the request only with MFA and without the insecure escape.
func Check(o Options) bool {
	return !o.AllowInsecure && o.RequireMFA
}
`,
				"internal/auth/serve.go": `package auth

import "example.com/probe/internal/policy"

// Serve reports whether the request is permitted.
func Serve(enabled bool) bool {
	return policy.Check(policy.Options{RequireMFA: enabled})
}
`,
			},
			Head: map[string]string{
				"internal/auth/serve.go": `package auth

import "example.com/probe/internal/policy"

// Serve reports whether the request is permitted.
func Serve(enabled bool) bool {
	return policy.Check(policy.Options{AllowInsecure: enabled})
}
`,
			},
			WantExit:      2,
			WantStdout:    []string{"tier: full-adversarial", "novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P21-same-diff-config-weakening",
			Class:   ClassAuthorizationWeakening,
			Summary: "one commit weakens an auth predicate, deletes its test, and raises the review threshold to 99",
			Base: map[string]string{
				"internal/auth/policy.go":      goStrictPolicy,
				"internal/auth/policy_test.go": goPolicyTwoTests,
			},
			Head: map[string]string{
				"internal/auth/policy.go":      goWeakPolicy,
				"internal/auth/policy_test.go": goPolicyOneTest,
				".no-mistakes.yaml": `slop:
  test_count_floor: false
  risk:
    single_review_threshold: 99
    full_adversarial_threshold: 100
`,
			},
			// Exit 2 is the gate refusing to validate: the base ref's
			// thresholds route an auth weakening to the full tier, and the
			// sandbox has no reviewer. The head config's 99 never applies.
			WantExit:      2,
			WantStdout:    []string{"tier: full-adversarial", "gate-config-drift", "test-capitulation"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P22-auth-weakening-and-deleted-test-at-the-lowest-tier",
			Class:   ClassAuthorizationWeakening,
			Summary: "the mechanical floor still fires when the operator asks for leak-scan-only",
			Base: map[string]string{
				"internal/auth/policy.go":      goStrictPolicy,
				"internal/auth/policy_test.go": goPolicyTwoTests,
			},
			Head: map[string]string{
				"internal/auth/policy.go":      goWeakPolicy,
				"internal/auth/policy_test.go": goPolicyOneTest,
			},
			Args:          []string{"--tier", "leak-scan-only"},
			WantExit:      1,
			WantStdout:    []string{"test-capitulation"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R3-uncommitted-config-disables-the-test-floor",
			Class:   ClassMandatoryCheckIntegrity,
			Summary: "a .no-mistakes.yaml the author never committed must not turn a mandatory check off",
			Base: map[string]string{
				"calc.go": `package calc

// Add returns the sum.
func Add(a, b int) int { return a + b }
`,
				"calc_test.go": `package calc

import "testing"

func TestAddZero(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Fatal("zero")
	}
}

func TestAddOne(t *testing.T) {
	if Add(1, 0) != 1 {
		t.Fatal("one")
	}
}
`,
			},
			Head: map[string]string{
				"calc_test.go": `package calc

import "testing"

func TestAddOne(t *testing.T) {
	if Add(1, 0) != 1 {
		t.Fatal("one")
	}
}
`,
			},
			Uncommitted: map[string]string{
				".no-mistakes.yaml": "slop:\n  test_count_floor: false\n",
			},
			Args:          []string{"--tier", "leak-scan-only"},
			WantExit:      1,
			WantStdout:    []string{"test-capitulation"},
			WantNotStdout: []string{"verdict: pass", "mandatory check: test-count floor disabled"},
		},
		{
			Name:    "R2-gitattributes-blinds-the-leak-scan",
			Class:   ClassMandatoryCheckIntegrity,
			Summary: "a committed -diff attribute must not turn the leak scan into a check that always reports clean",
			Base: map[string]string{
				".gitattributes": "NOTES.md -diff\n",
				"NOTES.md":       "# Notes\n\nnothing here yet\n",
			},
			Head: map[string]string{
				"NOTES.md": "# Notes\n\nAKIAIOSFODNN7EXAMPLE\n",
			},
			WantExit:      1,
			WantStdout:    []string{"leak-identity-scan"},
			WantNotStdout: []string{"verdict: pass", "leak scan completed (0 findings)"},
		},
		{
			Name:    "R2-control-no-gitattributes",
			Class:   ClassControl,
			Summary: "the same credential shape without the attribute is the control the blinding case is measured against",
			Base: map[string]string{
				"NOTES.md": "# Notes\n\nnothing here yet\n",
			},
			Head: map[string]string{
				"NOTES.md": "# Notes\n\nAKIAIOSFODNN7EXAMPLE\n",
			},
			WantExit:      1,
			WantStdout:    []string{"leak-identity-scan"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R4-default-repository-agents-instructions",
			Class:   ClassFleetInstructionRewrite,
			Summary: "a repository with no configuration at all must still protect AGENTS.md",
			Base: map[string]string{
				"AGENTS.md": agentsInstructionsStrict,
				"main.go":   "package main\n\nfunc main() {}\n",
			},
			Head: map[string]string{
				"AGENTS.md": agentsInstructionsWeak,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass", "Markdown-only changes do not reach runtime code"},
		},
		{
			Name:    "R4-shipped-config-agents-instructions",
			Class:   ClassFleetInstructionRewrite,
			Summary: "NoSlop's own shipped high_risk_paths must not leave its own instruction file unprotected",
			Base: map[string]string{
				".no-mistakes.yaml": `slop:
  risk:
    high_risk_paths:
      - internal/auth/**
      - internal/security/**
      - .github/workflows/**
      - migrations/**
`,
				"AGENTS.md": agentsInstructionsStrict,
				"main.go":   "package main\n\nfunc main() {}\n",
			},
			Head: map[string]string{
				"AGENTS.md": agentsInstructionsWeak,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R4-nested-skill-instructions",
			Class:   ClassFleetInstructionRewrite,
			Summary: "a nested SKILL.md is an instruction file at any depth",
			Base: map[string]string{
				"skills/release/SKILL.md": agentsInstructionsStrict,
				"main.go":                 "package main\n\nfunc main() {}\n",
			},
			Head: map[string]string{
				"skills/release/SKILL.md": agentsInstructionsWeak,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R4-claude-instructions",
			Class:   ClassFleetInstructionRewrite,
			Summary: "CLAUDE.md is the same surface as AGENTS.md",
			Base: map[string]string{
				"CLAUDE.md": agentsInstructionsStrict,
				"main.go":   "package main\n\nfunc main() {}\n",
			},
			Head: map[string]string{
				"CLAUDE.md": agentsInstructionsWeak,
			},
			WantExit:      2,
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R5-globstar-high-risk-path",
			Class:   ClassMandatoryCheckIntegrity,
			Summary: "an operator writing **/policy.md to mean any depth must get what they configured",
			Base: map[string]string{
				".no-mistakes.yaml": `slop:
  risk:
    high_risk_paths:
      - "**/policy.md"
`,
				"services/api/policy.md": "# Policy\n\nRequests need an approval.\n",
				"main.go":                "package main\n\nfunc main() {}\n",
			},
			Head: map[string]string{
				"services/api/policy.md": "# Policy\n\nApproval is optional.\n",
			},
			WantExit:      2,
			WantStdout:    []string{"high-risk"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R6-ratio-claim-is-deterministic",
			Class:   ClassOracleIntegrity,
			Summary: "the same true ratio claim must be judged the same way on every run",
			Base: map[string]string{
				"evidence/counts.json": evidenceCounts,
				"README.md":            "# Project\n",
			},
			Head: map[string]string{
				"outbound/post.md": "# Report\n\nThe passed to failed ratio was 9, see evidence/counts.json for the counts.\n",
			},
			Repeat:        16,
			WantExit:      0,
			WantStdout:    []string{"verdict: pass"},
			WantNotStdout: []string{"evidence-mismatch"},
		},
		{
			Name:    "R6-inverted-ratio-claim-is-deterministically-rejected",
			Class:   ClassOracleIntegrity,
			Summary: "the inverted ratio must be rejected on every run, not on some of them",
			Base: map[string]string{
				"evidence/counts.json": evidenceCounts,
				"README.md":            "# Project\n",
			},
			Head: map[string]string{
				"outbound/post.md": "# Report\n\nThe failed to passed ratio was 9, see evidence/counts.json for the counts.\n",
			},
			Repeat:        16,
			WantExit:      1,
			WantStdout:    []string{"evidence-mismatch"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R7-aggregate-ignores-the-named-fields",
			Class:   ClassOracleIntegrity,
			Summary: "a total that names two fields must be the total of those two fields",
			Base: map[string]string{
				"evidence/counts.json": evidenceCounts,
				"README.md":            "# Project\n",
			},
			Head: map[string]string{
				"outbound/post.md": "# Report\n\nThe passed and failed total was 60, see evidence/counts.json for the counts.\n",
			},
			WantExit:      1,
			WantStdout:    []string{"evidence-mismatch"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R7-control-correct-named-total",
			Class:   ClassControl,
			Summary: "the true total of the two named fields is still accepted",
			Base: map[string]string{
				"evidence/counts.json": evidenceCounts,
				"README.md":            "# Project\n",
			},
			Head: map[string]string{
				"outbound/post.md": "# Report\n\nThe passed and failed total was 20, see evidence/counts.json for the counts.\n",
			},
			WantExit:   0,
			WantStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R17-unnamed-evidence-cannot-verify-a-literal",
			Class:   ClassOracleIntegrity,
			Summary: "a cited file whose numbers carry no names cannot support a claim about any one of them",
			Base: map[string]string{
				"evidence/list.json": "[1,2,3,4,5]\n",
				"README.md":          "# Project\n",
			},
			Head: map[string]string{
				"outbound/post.md": "# Report\n\nWe shipped 4 fixes, see evidence/list.json for the list.\n",
			},
			WantExit:      1,
			WantStdout:    []string{"evidence-mismatch"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "R16-zero-entry-blocklist-reports-its-emptiness",
			Class:   ClassMandatoryCheckIntegrity,
			Summary: "a configured blocklist with no entries must not read like a populated one",
			Base: map[string]string{
				".no-mistakes.yaml": "slop:\n  leak_scan:\n    blocklist_file: .private-names\n",
				".private-names":    "# nothing yet\n\n",
				"README.md":         "# Project\n",
			},
			Head: map[string]string{
				"README.md": "# Project\n\nPlain update.\n",
			},
			WantExit:   0,
			WantStdout: []string{"0 entries"},
		},
		{
			Name:    "P1-selector-and-argument-swap",
			Class:   ClassControl,
			Summary: "the two shapes the previous review reported must stay blocked",
			Base: map[string]string{
				"policy.go": `package policy

import "strings"

// Owns reports whether the actor owns the resource.
func Owns(actor, resource string) bool {
	return strings.HasPrefix(actor, resource)
}

// Guard reports whether the actor may act on the resource.
func Guard(actor, resource string) bool {
	return Owns(actor, resource)
}
`,
			},
			Head: map[string]string{
				"policy.go": `package policy

import "strings"

// Owns reports whether the actor owns the resource.
func Owns(actor, resource string) bool {
	return strings.Contains(actor, resource)
}

// Guard reports whether the actor may act on the resource.
func Guard(actor, resource string) bool {
	return Owns(resource, actor)
}
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "P5-same-package-constant-swap",
			Class:   ClassControl,
			Summary: "a same-package constant swap must stay blocked by the collision oracle",
			Base: map[string]string{
				"level.go": `package level

// Level names an access level.
type Level int

// The access levels, ordered from least to most privileged.
const (
	LevelGuest Level = iota
	LevelAdmin
)

// IsAdmin reports whether the level is the administrative one.
func IsAdmin(level Level) bool {
	return level == LevelAdmin
}
`,
			},
			Head: map[string]string{
				"level.go": `package level

// Level names an access level.
type Level int

// The access levels, ordered from least to most privileged.
const (
	LevelGuest Level = iota
	LevelAdmin
)

// IsAdmin reports whether the level is the administrative one.
func IsAdmin(level Level) bool {
	return level == LevelGuest
}
`,
			},
			WantExit:      2,
			WantStdout:    []string{"novelty: 2"},
			WantNotStdout: []string{"verdict: pass"},
		},
		{
			Name:    "control-genuine-cross-file-rename-is-still-cheap",
			Class:   ClassControl,
			Summary: "a real rename campaign, where the new name is declared in the change itself, keeps its low novelty",
			Base: map[string]string{
				"lib/name.js": `export function oldHelperName(value) {
	return value + 1
}
`,
				"src/use.js": `import { oldHelperName } from '../lib/name.js'

export const helper = oldHelperName
`,
			},
			Head: map[string]string{
				"lib/name.js": `export function newHelperName(value) {
	return value + 1
}
`,
				"src/use.js": `import { newHelperName } from '../lib/name.js'

export const helper = newHelperName
`,
			},
			WantExit:   0,
			WantStdout: []string{"novelty: 0", "verdict: pass"},
		},
		{
			Name:    "control-plain-markdown-still-costs-nothing",
			Class:   ClassControl,
			Summary: "an ordinary README edit must not be dragged up a tier by the instruction-file markers",
			Base: map[string]string{
				"README.md": "# Project\n",
			},
			Head: map[string]string{
				"README.md": "# Project\n\nPlain update.\n",
			},
			WantExit:   0,
			WantStdout: []string{"tier: leak-scan-only", "verdict: pass"},
		},
	}
	return cases
}
