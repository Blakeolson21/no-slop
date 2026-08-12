# NoSlop replay corpus

The published [evaluation corpus reference](../docs/src/content/docs/reference/evaluation-corpus.md) owns the format and scorer contract. This directory keeps the recorded artifacts next to the command that replays them.

Each directory under `seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and independently expected findings.

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

The scorer reports found, missed, and false-positive counts. It does not launch a reviewer or invent policy output. Capturing live policy output, latency, and cost is a later integration.
