# NoSlop replay corpus

The published [evaluation corpus reference](../docs/src/content/docs/reference/evaluation-corpus.md) owns the format and scorer contract. This directory keeps the recorded artifacts next to the command that replays them.

Each directory under `seeds/` contains:

- `change.diff`: the recorded change presented to a review policy.
- `case.json`: schema version 1 metadata and independently expected findings.

The campaign contains 32 synthetic cases: three per reviewer lens, four leak cases, and four stale outbound-prose cases. Expected findings match on lens, path, and line. An expected line of `0` matches any line in that path.

`campaign.json` fixes each case's alias, intent, tier, conditioning input, and optional thread fixture. `policies/` contains the frozen conditioned, unconditioned, and challenge policies. `results/2026-08-12/` contains scorer inputs plus raw invocation and latency captures. [The measured report](../docs/evaluation.md) publishes the protocol, full result table, failures, and limitations.

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
