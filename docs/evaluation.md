# NoSlop corpus evaluation

Evaluation date: 2026-08-12

## Result

This campaign does not demonstrate that NoSlop is better than other reviewers. Both policies found 10 of 32 expected findings, missed 22, and emitted no unmatched findings. Provenance conditioning added eight reviewer invocations and 240.199 seconds of summed invocation time without finding anything extra.

The split is important: deterministic checks found all 10 cases they can evaluate, while the model-backed reviewer found none of the 22 remaining expectations because every reviewer invocation reached the fixed 30-second timeout.

| Policy | Found | Missed | False-positive | Recall | Reviewer timeouts | Summed invocation time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | 10 | 22 | 0 | 31.25% | 24 / 24 | 720.923 s |
| Conditioned | 10 | 22 | 0 | 31.25% | 32 / 32 | 961.122 s |
| Conditioned minus unconditioned | 0 | 0 | 0 | 0 points | +8 | +240.199 s |

`False-positive` means an emitted finding that did not match an expectation by lens, path, and line. The corpus has no clean negative-control cases, so zero unmatched findings is not a false-positive rate and should not be presented as one.

## Corpus and protocol

The public corpus contains 32 synthetic diffs:

- 24 reviewer cases, three for each of the eight taxonomy lenses
- 4 credential or identity leak cases
- 4 stale outbound-prose cases using recorded thread-state fixtures
- 16 `single-review`, 8 `full-adversarial`, and 8 `leak-scan-only` cases

Every `case.json` expectation was written from the case design before either policy ran. The expected-finding files had aggregate SHA-256 `41b17ad3447b955747b44965d6a03b831d3097305020169d8d3c5325ca6d4b65`. The fixed campaign manifest had SHA-256 `6304092d8ac3a41d68f0dd60f0c1e0c5b235c255ec6d314a6972d0305053cf1b`.

The unconditioned policy applies the complete taxonomy with no history-selected priority. It uses one reviewer round for `single-review` and two linked rounds for `full-adversarial`. The conditioned policy supplies the case lens as repeated provenance, reviews it first, and raises `single-review` to two linked rounds. `full-adversarial` remains two rounds. The frozen policy files had aggregate SHA-256 `38ea6194194aef6e3a7e5cba755766408df1840e88b6022d61615fa99f3cb9d0`.

Both runs used `opencode/deepseek-v4-flash-free` through OpenCode 1.1.34, sequential execution, no retries, and a 30-second timeout per reviewer invocation. A timeout records an empty response and remains a miss. Deterministic leak, test-count, and prose checks replay directly from the diff and recorded thread fixture without reading the expectation.

The test-count harness initially omitted its mandatory probe. That harness defect was caught after the first captures. The probe was added test-first, and the deterministic portion of both captures was refreshed without retrying or replacing any reviewer timeout. No diff, expectation, policy, reviewer response, or timeout was changed.

## Results by lens and tier

Each score cell is `found / missed / false-positive`. The two policies have identical score cells.

| Tier | Lens or finding class | Expected | Unconditioned | Conditioned |
| --- | --- | ---: | ---: | ---: |
| leak-scan-only | `leak-identity-scan` | 4 | 4 / 0 / 0 | 4 / 0 / 0 |
| leak-scan-only | `thread-closed` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| leak-scan-only | `duplicate-claim` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `vacuous-check` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `test-capitulation` | 2 | 1 / 1 / 0 | 1 / 1 / 0 |
| single-review | `self-consistent-oracle` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `comment-defended-workaround` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `scope-expansion` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `asserted-followup-without-artifact` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `fail-open-default` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| single-review | `rule-applied-in-one-place-not-sibling` | 2 | 0 / 2 / 0 | 0 / 2 / 0 |
| full-adversarial | `vacuous-check` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `test-capitulation` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `self-consistent-oracle` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `comment-defended-workaround` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `scope-expansion` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `asserted-followup-without-artifact` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `fail-open-default` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| full-adversarial | `rule-applied-in-one-place-not-sibling` | 1 | 0 / 1 / 0 | 0 / 1 / 0 |
| **Total** | | **32** | **10 / 22 / 0** | **10 / 22 / 0** |

The two `test-capitulation` hits came from the deterministic test-count floor, not the model-backed reviewer. Thus the deterministic subset scored 10 / 0 / 0 and the reviewer-dependent subset scored 0 / 22 / 0.

## Latency by tier

Times below are summed invocation wall time. Packets group cases by lens, so the invocation count is the useful denominator. Mandatory checks completed below the timer's one-millisecond resolution and contribute 0 ms in the capture.

| Policy | Corpus tier | Reviewer invocations | Timeouts | Total | Mean | Range |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | single-review | 8 | 8 | 240.306 s | 30.038 s | 30.036 to 30.040 s |
| Unconditioned | full-adversarial | 16 | 16 | 480.617 s | 30.038 s | 30.036 to 30.043 s |
| Conditioned | single-review | 16 | 16 | 480.560 s | 30.035 s | 30.032 to 30.040 s |
| Conditioned | full-adversarial | 16 | 16 | 480.562 s | 30.035 s | 30.032 to 30.039 s |

## Failures and limitations

- All 56 reviewer invocations timed out. This run measures the complete configured policy, including endpoint availability, but cannot separate model quality from provider availability.
- The corpus is synthetic. It is replayable and expectation-first, but it does not establish performance on naturally occurring pull requests.
- There is one expected finding per case. Multi-defect recall is unmeasured.
- There are no clean negative controls, so precision and false-positive rate remain unmeasured.
- No competitor was run on this corpus. These results cannot support a comparative superiority claim.
- Model token usage and monetary cost were not reported by the endpoint. Latency is the only recorded resource measure.
- Findings match exactly on lens and path, and on line unless the expected line is 0. Semantically similar findings under another label count as both a miss and an unmatched finding.

## Replay

Capture fresh policy results:

```sh
NOSLOP_EVAL_MODEL=opencode/deepseek-v4-flash-free \
NOSLOP_EVAL_TIMEOUT_SECONDS=30 \
./scripts/capture-noslop-corpus.sh unconditioned corpus/results/replay/unconditioned.json

NOSLOP_EVAL_MODEL=opencode/deepseek-v4-flash-free \
NOSLOP_EVAL_TIMEOUT_SECONDS=30 \
./scripts/capture-noslop-corpus.sh conditioned corpus/results/replay/conditioned.json
```

Score checked-in captures:

```sh
./bin/noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results corpus/results/2026-08-12/unconditioned.json \
  --conditioned-results corpus/results/2026-08-12/conditioned.json
```

The checked-in scorer captures are `conditioned.json` and `unconditioned.json`. Their matching `.run.json` files retain policy name, model, timeout, case aliases, tier, priority lens, round, raw response, error, and elapsed milliseconds for every invocation.

Checked-in result SHA-256 values:

| File | SHA-256 |
| --- | --- |
| `conditioned.json` | `987e6c7b931297a264098a0c108e90ac7a20586bc59d71a756142c702b61c8ad` |
| `conditioned.run.json` | `d1fd6371259e88cceaaaee747707b57e1a03e85396060b6525781080ca209fd9` |
| `unconditioned.json` | `c3e3d068aa30d9f2012201a21c59b35c45f5fb3917711b7de2af1156fbce06e8` |
| `unconditioned.run.json` | `12e10bb341253d768c7b11a2c3a7aa3baadeb618f09a654d4c5465e3ade3fbb8` |

These hashes identify the exact checked-in captures. Re-running the campaign produces a new dated observation and should not overwrite this snapshot.
