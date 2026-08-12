---
title: NoSlop Evaluation Corpus
description: Replay format and scoring for conditioned and unconditioned policy findings.
---

The NoSlop corpus stores recorded diffs with independent expectations. The current public campaign has 36 synthetic cases covering every reviewer lens plus deterministic leak and stale outbound-prose checks. Three positive cases exercise the `redundant-comment` subclasses, and one clean negative case preserves a genuine constraint comment. The checked-in `2026-08-12` baseline found 10 and missed 22; the `2026-08-12-r4` capture found all 32 original expectations after adding shared deterministic lens pre-checks. Each directory under `corpus/seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and expected findings.

Expected findings match on lens, path, and line. An expected line of `0` matches any line in that path. A clean negative case states its expectation first as an explicit empty `expected_findings` array; omitting the field is invalid. `corpus/campaign.json` records the fixed alias, intent, tier, conditioning input, and optional thread fixture for each case. The policy prompts and raw dated captures live alongside the cases under `corpus/`.

Policy result files use this shape:

```json
{
  "schema_version": 1,
  "policy": "conditioned",
  "cases": [
    {
      "case_id": "fail-open-default-missing-policy",
      "findings": [
        {
          "lens": "fail-open-default",
          "path": "policy.go",
          "line": 8,
          "description": "an unreadable policy becomes allow"
        }
      ]
    }
  ]
}
```

Capture fresh policy results, then compare them:

```sh
./scripts/capture-noslop-corpus.sh unconditioned corpus/results/replay/unconditioned.json
./scripts/capture-noslop-corpus.sh conditioned corpus/results/replay/conditioned.json
```

```sh
noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results corpus/results/replay/unconditioned.json \
  --conditioned-results corpus/results/replay/conditioned.json
```

The capture script keeps raw reviewer responses, timeouts, and elapsed milliseconds in matching `.run.json` files. It replays deterministic checks from the diff, campaign intent, and optional thread fixture without reading case expectations, then merges reviewer findings by lens, path, and line. The scorer labels the corpus as synthetic replay cases, prints each policy name and result-file path, and states that the captures were not produced by the current run. It then reports found, missed, and false-positive counts. It refuses missing cases, unknown expected finding classes, missing diffs, malformed formats, and duplicate results. It does not launch a reviewer or invent policy output.

## Historical case sets

The public corpus grows between campaigns, so a capture recorded against an earlier corpus is scored against the cases it actually ran. A manifest under `corpus/case-sets/` pins that membership:

```json
{
  "schema_version": 1,
  "name": "rounds-1-through-4",
  "case_ids": ["asserted-followup-ticket-without-id", "fail-open-default-missing-blocklist"],
  "content_sha256": "<aggregate digest of the selected case content>"
}
```

Pass it with `--case-set`:

```sh
noslop evaluate \
  --corpus corpus/seeds \
  --case-set corpus/case-sets/rounds-1-through-4.json \
  --unconditioned-results corpus/results/2026-08-12-r4/unconditioned.json \
  --conditioned-results corpus/results/2026-08-12-r4/conditioned.json
```

`case_ids` selects those cases from the loaded corpus in manifest order, and `content_sha256` covers the selected `case.json` and `change.diff` content. An unsupported schema version, a missing name, an empty, duplicate, or unknown case id, and edited or removed snapshot content all fail scoring rather than silently rescoring a capture against a corpus it never saw. A capture of the current corpus needs no manifest.
