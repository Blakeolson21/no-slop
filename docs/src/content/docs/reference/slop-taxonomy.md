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

Mechanical pre-check: Test-count floor plus numeric tolerance comparison. NoSlop counts recognizable test declarations in the changed files at the base and head revisions and detects a changed `>` threshold that became numerically larger. Either finding blocks at every tier, including an operator override, even when the configured test command passes.

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

Mechanical pre-check: With `--intent`, detect a new runtime or schema file that contradicts an explicit scope limit such as wording-only, header-only, or no schema change. A new subsystem named by the intent is acquitted. Without stated intent, this pre-check emits nothing.

## Asserted follow-up without artifact

Name: `asserted-followup-without-artifact`

Description: The change says a follow-up exists but provides no inspectable artifact.

Reviewer guidance: Verify claims such as filed, tracked, assigned, approved, scheduled, or deferred against the artifacts available to the review. Require a URL, issue number, ticket ID, or approval reference. A comment that promises later work is not evidence that the work has an owner or durable reference.

Mechanical pre-check: Flag a newly added comment that asserts filed, tracked, assigned, approved, or scheduled work without a URL or recognizable issue, ticket, or approval reference.

## Fail-open default

Name: `fail-open-default`

Description: An unknown, failed, timed-out, or unparsed state becomes permission to continue.

Reviewer guidance: Follow error, empty, timeout, parse-failure, and default branches. Flag paths where could-not-determine becomes `nil, nil`, true, allow, ready, healthy, empty findings, a privileged object, or another permissive result without an explicit policy.

Mechanical pre-check: Detect a newly added permissive return within an error, not-found, timeout, or unreadable-state branch. A nearby comment that names an explicit permissive policy prevents a mechanical finding and leaves the judgment to review.

## Rule applied in one place, not its sibling

Name: `rule-applied-in-one-place-not-sibling`

Description: A rule fixes one path while equivalent sibling paths remain exposed.

Reviewer guidance: Find explicit/configured paths, transports, providers, platforms, formats, versioned routes, and state transitions that enforce the same invariant. Compare strict and permissive branches explicitly. Prefer the earliest shared owner when one exists. When repetition is intentional, verify every sibling is covered and that new siblings cannot silently omit the rule.

Mechanical pre-check: Detect an explicit path failing beside a configured path returning empty success, one transport denying beside another allowing, and one versioned route gaining validation while its equivalent sibling remains unvalidated.

## Redundant comment

Name: `redundant-comment`

Description: A comment repeats itself or says only what the adjacent code already states.

Reviewer guidance: A comment should state only what the code cannot. Flag repeated phrases within one comment, comments whose words merely restate the next code line, and doc comments that add no information beyond the adjacent declaration name. Naming the declaration is conventional and is not by itself redundant. Preserve comments that record genuine constraints, rationale, invariants, or external requirements that the implementation cannot express.

Mechanical pre-check: On Go comment blocks the change touched, detect a substantive clause repeated as the next clause or after the adjacent declaration name, standalone comments whose every meaningful word occurs in the adjacent code line, and a doc comment whose every informative word is already spelled by the declaration it documents. Repetition embedded in different constraints, contrasts, or qualifications stays out of the mechanical blocker. Go syntax associates documentation with functions, methods, and grouped or ungrouped type, variable, and constant declarations, including declarations that introduce multiple names. Inline comments participate only in internal-phrase detection. Contiguous standalone comment lines are judged as the one comment a reader sees, a blank line ends a comment's attachment to the code below it, and indented code samples inside a comment are excluded. Naming the declaration is not itself the signal, because Go's own convention is that a doc comment opens with the declaration name. Operator normalization treats increment and decrement wording as equivalent to their compound-assignment forms. Existing comments are not scanned merely because adjacent code changed. When Git cannot distinguish a same-directory Go deletion and addition from a rewrite, unchanged matching comments are deferred to mandatory full review instead of guessed mechanically.

## Mandatory leak and identity scan

The leak and identity scan is an artifact-wide mechanical check, not a reviewer-only lens. It runs at every tier before any optional reviewer or test command.

The scanner checks common credential shapes, private key headers, personal home paths, and user-configured identity markers. Findings never reproduce the matched value. Private names come from `.noslop-blocklist` by default, with one entry per line. Keep the real blocklist outside version control and add the names that are private in your own environment. When the built-in default file is absent, the scanner says that no private-name list is active and continues with its built-in credential patterns.

A configured missing blocklist and an unreadable blocklist stop evaluation. Intentional fixture literals can carry `noslop:allow-leak` on the same source line. Every honored marker prints its file and line and counts in the verdict summary. Repositories can set `slop.leak_scan.allow_exemptions: false` to turn every marker into a blocking finding.

Every completed verdict reports the status and finding count for the lens pre-check, leak scan, test-count floor, and prose oracle. A disabled test-count floor is stated explicitly. The other lens pre-checks run at every tier.

NoSlop ships generic placeholder entries only. It does not ship any operator's actual hostnames, codenames, project names, or identity data.

## Provenance conditioning

Provenance history does not add another lens. It changes the policy applied to the same catalog. When one generating lane and model accumulates three net accepted findings for a lens in its last 10 changes, NoSlop raises the tier by one level and reviews repeated lenses first. Repeated `test-capitulation` findings can also enable the configurable test-count floor when its static repository setting is off; the other conservative lens pre-checks always run.

No matching history preserves the unconditioned v1 route and prints that default. Unreadable or malformed history selects `full-adversarial` because the policy could not establish that a lighter tier is safe. A lower `--tier` that contradicts provenance is refused unless the operator also supplies `--force-tier`; the output prints both the escalation and any forced override.
