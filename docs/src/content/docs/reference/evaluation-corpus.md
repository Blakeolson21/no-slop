---
title: NoSlop Evaluation Corpus
description: Replay format and scoring for conditioned and unconditioned policy findings.
---

The NoSlop corpus stores recorded diffs with independent expected findings. The current public campaign has 32 synthetic cases covering every reviewer lens plus deterministic leak and stale outbound-prose checks. The checked-in `2026-08-12` baseline found 10 and missed 22; the `2026-08-12-r4` capture found all 32 after adding shared deterministic lens pre-checks. No case or expectation changed between those captures. Each directory under `corpus/seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and expected findings.

Expected findings match on lens, path, and line. An expected line of `0` matches any line in that path. `corpus/campaign.json` records the fixed alias, intent, tier, conditioning input, and optional thread fixture for each case. The policy prompts and raw dated captures live alongside the cases under `corpus/`.

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
