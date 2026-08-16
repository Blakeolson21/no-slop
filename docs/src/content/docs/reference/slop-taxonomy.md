---
title: NoSlop Taxonomy
description: Named failure lenses for AI-authored changes.
---

# NoSlop taxonomy

NoSlop reviews AI-authored changes through named failure lenses. A lens is not a verdict by itself. The reviewer must connect it to a concrete path, violated invariant, or unsupported claim in the current change.

## Vacuous check

Name: `vacuous-check`

Description: A check looks protective but cannot disagree with the value it validates.

Reviewer guidance: Trace both sides of assertions and guards to independent sources. Look for the same expression on both sides, a comparison helper given identical arguments, a saved value created after mutation, a copy that still shares the relevant state, or a predicate made unreachable by an earlier assignment.

Mechanical pre-check: Same-expression comparisons and comparison-helper arguments, plus previous/before snapshots assigned after their source was mutated and then compared.

## Test capitulation

Name: `test-capitulation`

Description: Tests were changed to accept the implementation instead of preserving the required behavior.

Reviewer guidance: Compare test strength with the base revision. Look for deleted cases, skipped tests, numeric tolerances changed to larger thresholds, weaker assertions, changed expected values without independent evidence, and coverage removed from an important branch. A passing suite is not proof when the suite became easier to pass.

Mechanical pre-check: Test-count floor plus numeric tolerance comparison. NoSlop counts recognizable test declarations in the changed files at the base and head revisions and detects a changed `>` threshold that became numerically larger. Both revisions are counted through the same code-only view the removed-guard detector uses, with comments and multi-line string literals blanked, so a deleted test kept as inert text does not keep the count level. Either finding blocks at every tier, including an operator override, even when the configured test command passes.

## Self-consistent oracle

Name: `self-consistent-oracle`

Description: A test oracle repeats the implementation and can only confirm the same mistake twice.

Reviewer guidance: Inspect expected vectors, formulas, fixtures, snapshots, and helper functions. Look for an independent literal or standard vector replaced by the production helper or the same expression used for the actual result. Require expected results from a specification, known example, independently produced fixture, or worked literal.

Mechanical pre-check: In test files, detect an expected-value assignment whose base literal or standard vector was replaced by arithmetic over runtime values or the same helper expression used to assign the actual result. Unchanged helpers and independently constructed expected objects do not match.

## Comment-defended workaround

Name: `comment-defended-workaround`

Description: An explanatory comment is used to legitimize a workaround instead of resolving its design cost.

Reviewer guidance: Treat comments that justify bypasses, duplication, special cases, or disabled validation as design signals. Connect the comment to its nearby permissive return, stale-data fallback, or security bypass. Verify that the workaround is necessary, bounded, and owned. The comment explains the choice but does not prove the choice is safe.

Mechanical pre-check: A narrow justification vocabulary must appear next to a permissive return or an explicit security bypass. A similar comment paired with a fail-closed action does not match.

## Scope expansion

Name: `scope-expansion`

Description: A fix quietly adds behavior or infrastructure beyond the requested change.

Reviewer guidance: Compare every new file and subsystem with the stated intent and original failing path. Flag unrelated cleanup, new features, generalized frameworks, schema work, or enforcement systems that were not needed for the smallest correct fix. Do not flag a shared fix merely because it touches multiple callers when that is the actual invariant owner.

Mechanical pre-check: With `--intent`, detect a new runtime or schema file that contradicts an explicit scope limit such as wording-only, header-only, or no schema change. A new subsystem named by the intent is acquitted. The check can only act on an intent that states a scope limit it recognizes, so a missing intent and an intent with no such limit both report the check as not armed rather than letting it contribute a silent zero.

## Asserted follow-up without artifact

Name: `asserted-followup-without-artifact`

Description: The change says a follow-up exists but provides no inspectable artifact.

Reviewer guidance: Verify claims such as filed, tracked, assigned, approved, scheduled, or deferred against the artifacts available to the review. Require a URL, issue number, ticket ID, or approval reference. A comment that promises later work is not evidence that the work has an owner or durable reference.

Mechanical pre-check: Flag a newly added comment that asserts filed, tracked, assigned, approved, or scheduled work without a URL or recognizable issue, ticket, or approval reference.

## Fail-open default

Name: `fail-open-default`

Description: An unknown, failed, timed-out, or unparsed state becomes permission to continue.

Reviewer guidance: Follow error, empty, timeout, parse-failure, and default branches. Flag paths where could-not-determine becomes `nil, nil`, true, allow, ready, healthy, empty findings, a privileged object, or another permissive result without an explicit policy.

Mechanical pre-check: Two detectors. The first flags a newly added permissive return within an error, not-found, timeout, or unreadable-state branch. It has no comment-based suppression: a comment declaring the permissive branch intentional is written by the party being checked and is verified against nothing, so it never stands the detector down.

The second asks whether the change dropped a refusing check, and asks it across the whole change set by clause identity rather than by counting guards. A guard clause the change stopped carrying is excused only when the same clause and refusing action is added somewhere in the change set, and each relocated clause is spent once, so unrelated new guards never pay for a deletion. Both revisions are read through a code-only view with comments and multi-line string literals blanked, so a deleted guard kept as inert text still reads as removed. Every unmatched clause is reported with its base-revision path and line and a short digest of the clause, never the clause text, because the removed clause may be exactly the credential the change deleted. This finding blocks at every tier and has no exemption path; an in-place reword, a deletion of dead code, and a consolidation of two checks into one all pay that price by design.

## Rule applied in one place, not its sibling

Name: `rule-applied-in-one-place-not-sibling`

Description: A rule fixes one path while equivalent sibling paths remain exposed.

Reviewer guidance: Find explicit/configured paths, transports, providers, platforms, formats, versioned routes, and state transitions that enforce the same invariant. Compare strict and permissive branches explicitly. Prefer the earliest shared owner when one exists. When repetition is intentional, verify every sibling is covered and that new siblings cannot silently omit the rule.

Mechanical pre-check: Detect an explicit path failing beside a configured path returning empty success, one transport denying beside another allowing, and one versioned route gaining validation while its equivalent sibling remains unvalidated.

## Redundant comment

Name: `redundant-comment`

Description: A comment repeats itself or says only what the adjacent code already states.

Reviewer guidance: A comment should state only what the code cannot. Flag repeated phrases within one comment, comments whose words merely restate the next code line, and doc comments that add no information beyond the adjacent declaration name. Naming the declaration is conventional and is not by itself redundant. Preserve comments that record genuine constraints, rationale, invariants, or external requirements that the implementation cannot express.

Mechanical pre-check: On Go comment blocks the change touched, detect a substantive clause repeated as the next clause or after the adjacent declaration name, standalone comments whose every meaningful word occurs in the adjacent code line, and a doc comment whose every informative word is already spelled by the declaration it documents. Repetition embedded in different constraints, contrasts, or qualifications stays out of the mechanical blocker. Go syntax associates documentation with functions, methods, and grouped or ungrouped type, variable, and constant declarations, including declarations that introduce multiple names. Inline comments participate only in internal-phrase detection. Contiguous standalone comment lines are judged as the one comment a reader sees, a blank line ends a comment's attachment to the code below it, and indented code samples inside a comment are excluded. Naming the declaration is not itself the signal, because Go's own convention is that a doc comment opens with the declaration name. Operator normalization treats increment and decrement wording as equivalent to their compound-assignment forms. Existing comments are not scanned merely because adjacent code changed. Git-added comments remain mechanically scanned at every tier. The loader does not infer lineage from matching comment text; only Git-reported renames link different paths.

## Mandatory leak and identity scan

The leak and identity scan is an artifact-wide mechanical check, not a reviewer-only lens. It runs at every tier before any optional reviewer or test command.

The scanner checks common credential shapes, private key headers, personal home paths, and user-configured identity markers. Findings never reproduce the matched value. Private names come from `.noslop-blocklist` by default, with one entry per line. Keep the real blocklist outside version control and add the names that are private in your own environment. When the built-in default file is absent, the scanner says that no private-name list is active and continues with its built-in credential patterns.

A configured missing blocklist and an unreadable blocklist stop evaluation. Inline `noslop:allow-leak` markers are rejected by default and become blocking findings, because a change that exempts its own credential with a trailing comment has not been leak scanned. A repository with genuine credential-shaped fixtures sets `slop.leak_scan.allow_exemptions: true` at the base ref; every honored marker then prints its file and line with the number of findings it actually suppressed.

Whether a blob is read as text or through the binary-safe renderings is decided from the blob's own bytes, never from git's rendering of the diff. Git samples the first 8000 bytes of a file to decide whether to emit hunks, so keying on git's output meant one NUL past that offset made both git and the scanner call the blob text, the credential pattern failed across the NUL, and the check reported zero findings over a live key. Committed `.gitattributes`, the uncommitted `.git/info/attributes`, and diff rendering therefore have no influence at all over whether leak scanning happens. A blob carrying a NUL or another non-whitespace control byte is scanned whole through both renderings and reported as reduced coverage.

Every completed verdict reports the status and finding count for the lens pre-check, leak scan, test-count floor, and prose oracle. A check that could not look reports `not armed` and a check that looked at less than everything reports `reduced coverage`, so a silent zero is never read as a clean result. A disabled test-count floor is stated explicitly. The other lens pre-checks run at every tier.

NoSlop ships generic placeholder entries only. It does not ship any operator's actual hostnames, codenames, project names, or identity data.

## Provenance conditioning

Provenance history does not add another lens. It changes the policy applied to the same catalog. When one generating lane and model accumulates three accepted findings for a lens in its retained history, NoSlop raises the tier by one level and reviews repeated lenses first. Repeated `test-capitulation` findings can also enable the configurable test-count floor when its static repository setting is off; the other conservative lens pre-checks always run.

The retention rule, what clears a lens score, and how an unverifiable lane identity is treated are owned by the [repository config reference](/no-slop/reference/repo-config/#slop). No matching history preserves the unconditioned v1 route and prints that default. Unreadable or malformed history selects `full-adversarial` because the policy could not establish that a lighter tier is safe.
