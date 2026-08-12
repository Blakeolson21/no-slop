---
title: NoSlop Evaluation Corpus
description: Replay format and scoring for conditioned and unconditioned policy findings.
---

The NoSlop corpus stores recorded diffs with independent expected findings. Each directory under `corpus/seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and expected findings.

Each seed exercises one NoSlop taxonomy lens. Expected findings match on lens, path, and line. An expected line of `0` matches any line in that path.

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

Capture one results file from each policy, then compare them:

```sh
noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results results/unconditioned.json \
  --conditioned-results results/conditioned.json
```

The scorer labels the corpus as synthetic replay cases, prints each policy name and result-file path, and states that the captures were not produced by the current run. It then reports found, missed, and false-positive counts. It refuses missing cases, unknown expected lenses, missing diffs, malformed formats, and duplicate results. It does not launch a reviewer or invent policy output. Capturing live policy output, latency, and cost is a later integration.
