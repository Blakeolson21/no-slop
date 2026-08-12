# NoSlop replay corpus

The published [evaluation corpus reference](../docs/src/content/docs/reference/evaluation-corpus.md) owns the format and scorer contract. This directory keeps the recorded artifacts next to the command that replays them.

Each directory under `seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and independently expected findings.

The campaign contains 32 synthetic cases: three per reviewer lens, four leak cases, and four stale outbound-prose cases. Expected findings match on lens, path, and line. An expected line of `0` matches any line in that path.

`campaign.json` fixes each case's alias, intent, tier, conditioning input, and optional thread fixture. `policies/` contains the conditioned, unconditioned, and challenge policies. `results/2026-08-12/` preserves the 10-found/22-missed baseline, while `results/2026-08-12-r4/` preserves the 32-found/0-missed round 4 capture. Both directories contain scorer inputs plus raw invocation and latency records. [The measured report](../docs/evaluation.md) keeps the before/after protocol, full result tables, failures, and limitations.

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

Capture fresh results from each policy:

```sh
./scripts/capture-noslop-corpus.sh unconditioned corpus/results/replay/unconditioned.json
./scripts/capture-noslop-corpus.sh conditioned corpus/results/replay/conditioned.json
```

Then compare them:

```sh
noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results corpus/results/replay/unconditioned.json \
  --conditioned-results corpus/results/replay/conditioned.json
```

The capture script runs deterministic checks from recorded inputs, invokes the selected reviewer policy without reading expectations, applies a fixed per-invocation timeout, and keeps raw run metadata. The scorer reports found, missed, and false-positive counts. It does not launch a reviewer or invent policy output.
