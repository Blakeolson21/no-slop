---
title: NoSlop Taxonomy
description: Named failure lenses for AI-authored changes.
---

# NoSlop taxonomy

NoSlop reviews AI-authored changes through named failure lenses. A lens is not a verdict by itself. The reviewer must connect it to a concrete path, violated invariant, or unsupported claim in the current change.

## Vacuous check

Name: `vacuous-check`

Description: A check looks protective but cannot disagree with the value it validates.

Reviewer guidance: Trace both sides of assertions and guards to independent sources. Look for a value compared with itself, a saved value created after mutation, a copy that still shares the relevant state, or a predicate made unreachable by an earlier assignment.

Mechanical pre-check: None in v1. Syntax alone cannot reliably establish whether two expressions have independent provenance.

## Test capitulation

Name: `test-capitulation`

Description: Tests were changed to accept the implementation instead of preserving the required behavior.

Reviewer guidance: Compare test strength with the base revision. Look for deleted cases, skipped tests, wider tolerances, weaker assertions, changed expected values without independent evidence, and coverage removed from an important branch. A passing suite is not proof when the suite became easier to pass.

Mechanical pre-check: Test-count floor. NoSlop counts recognizable test declarations in the changed files at the base and head revisions. A lower head count blocks the gate at every tier, including an operator override, even when the configured test command passes.

## Self-consistent oracle

Name: `self-consistent-oracle`

Description: A test oracle repeats the implementation and can only confirm the same mistake twice.

Reviewer guidance: Inspect expected vectors, formulas, fixtures, snapshots, and helper functions. Require expected results from a specification, known example, independently produced fixture, or worked literal. Production code and its test should not derive truth through the same algorithm.

Mechanical pre-check: None in v1. Code similarity is useful evidence but not enough to distinguish a copied oracle from a legitimate shared standard.

## Comment-defended workaround

Name: `comment-defended-workaround`

Description: An explanatory comment is used to legitimize a workaround instead of resolving its design cost.

Reviewer guidance: Treat comments that justify bypasses, duplication, special cases, or disabled validation as design signals. Verify that the workaround is necessary, bounded, and owned. The comment explains the choice but does not prove the choice is safe.

Mechanical pre-check: None in v1.

## Scope expansion

Name: `scope-expansion`

Description: A fix quietly adds behavior or infrastructure beyond the requested change.

Reviewer guidance: Compare the diff with the stated intent and original failing path. Flag unrelated cleanup, new features, generalized frameworks, or enforcement systems that were not needed for the smallest correct fix. Do not flag a shared fix merely because it touches multiple callers when that is the actual invariant owner.

Mechanical pre-check: None in v1.

## Asserted follow-up without artifact

Name: `asserted-followup-without-artifact`

Description: The change says a follow-up exists but provides no inspectable artifact.

Reviewer guidance: Verify claims such as filed, tracked, scheduled, or deferred against the artifacts available to the review. A comment that promises later work is not evidence that the work has an owner or durable reference.

Mechanical pre-check: None in v1.

## Fail-open default

Name: `fail-open-default`

Description: An unknown, failed, timed-out, or unparsed state becomes permission to continue.

Reviewer guidance: Follow error, empty, timeout, parse-failure, and default branches. Flag paths where could-not-determine becomes allow, pass, ready, empty findings, or another permissive result without an explicit policy. Pay particular attention to fallback values that collapse unknown into false.

Mechanical pre-check: None in v1.

## Rule applied in one place, not its sibling

Name: `rule-applied-in-one-place-not-sibling`

Description: A rule fixes one path while equivalent sibling paths remain exposed.

Reviewer guidance: Find sibling adapters, handlers, formats, platforms, and state transitions that enforce the same invariant. Prefer the earliest shared owner when one exists. When repetition is intentional, verify every sibling is covered and that new siblings cannot silently omit the rule.

Mechanical pre-check: None in v1.

## Mandatory leak and identity scan

The leak and identity scan is an artifact-wide mechanical check, not a reviewer-only lens. It runs at every tier before any optional reviewer or test command.

The scanner checks common credential shapes, private key headers, personal home paths, and user-configured identity markers. Findings never reproduce the matched value. Private names come from `.noslop-blocklist` by default, with one entry per line. Keep the real blocklist outside version control and add the names that are private in your own environment.

A configured blocklist that cannot be read stops evaluation. Intentional fixture literals can carry `noslop:allow-leak` on the same source line. The exemption applies only to that line.

NoSlop ships generic placeholder entries only. It does not ship any operator's actual hostnames, codenames, project names, or identity data.
