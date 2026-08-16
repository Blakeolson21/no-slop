# NoSlop Corpus Evaluation

Evaluation dates: 2026-08-12 baseline, round 4, and round 5

## Round 5 result

Round 5 adds the mechanical `redundant-comment` lens and expands the public campaign from 32 to 36 cases. Both policy captures found all 35 positive expectations with no misses or unmatched findings. The three new positive cases independently cover repeated n-grams within one comment, comment-to-next-code token overlap, and a doc comment that repeats its test or function name verbatim. The fourth case is an explicit clean negative: a comment that records a signature/wire-order constraint emitted no finding.

The new check runs on added comment lines in the mandatory lens pre-check, including the `leak-scan-only` tier. The capture used a one-second reviewer timeout because the change is deterministic; all 24 unconditioned and 32 conditioned reviewer invocations timed out, while mandatory replay supplied the 35 findings. This remains a synthetic regression result. One clean negative is useful evidence for the named constraint shape, not a general false-positive rate.

| Policy | Cases | Positive expectations | Found | Missed | Unmatched findings | Clean negatives passed | Reviewer timeouts | Summed invocation time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | 36 | 35 | 35 | 0 | 0 | 1 / 1 | 24 / 24 | 24.885 s |
| Conditioned | 36 | 35 | 35 | 0 | 0 | 1 / 1 | 32 / 32 | 33.189 s |

The three new positives and the clean negative all use `leak-scan-only`, directly exercising the least expensive route. The original 32 cases remain mechanically green.

## Round 4 result

Round 4 found all 32 expected findings under both policies, with no unmatched findings in this corpus. That is a 22-finding improvement over the honest baseline and raises measured synthetic-corpus recall from 31.25% to 100%. The result comes entirely from the new deterministic lens pre-checks: every one of the 56 fresh reviewer invocations still timed out, so the sharpened reviewer guidance had no opportunity to add a finding.

This is a corpus regression result, not evidence that NoSlop is better than another reviewer or that it has perfect real-world recall. The corpus remains synthetic and has no clean negative-control cases, so zero unmatched findings is not a false-positive rate.

### Before and after

| Campaign | Policy | Found | Missed | False-positive | Recall | Reviewer timeouts | Summed invocation time |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Baseline | Unconditioned | 10 | 22 | 0 | 31.25% | 24 / 24 | 720.923 s |
| Baseline | Conditioned | 10 | 22 | 0 | 31.25% | 32 / 32 | 961.122 s |
| Round 4 | Unconditioned | 32 | 0 | 0 | 100% | 24 / 24 | 720.807 s |
| Round 4 | Conditioned | 32 | 0 | 0 | 100% | 32 / 32 | 961.245 s |

| Policy | Found delta | Missed delta | Recall delta | False-positive delta | Timeout delta |
| --- | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | +22 | -22 | +68.75 points | 0 | 0 |
| Conditioned | +22 | -22 | +68.75 points | 0 | 0 |

Conditioning still produced no incremental finding. In round 4 it added eight timeouts and 240.438 seconds of summed invocation time. The 32 findings in each capture consist of the original 10 leak, test-count, and prose findings plus 22 new deterministic lens findings. Reviewer incremental yield remained zero.

### Round 4 results by lens and tier

Each score cell is `found / missed / false-positive`. Both policies have the same cells.

| Tier | Lens or finding class | Expected | Unconditioned | Conditioned |
| --- | --- | ---: | ---: | ---: |
| leak-scan-only | `leak-identity-scan` | 4 | 4 / 0 / 0 | 4 / 0 / 0 |
| leak-scan-only | `thread-closed` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| leak-scan-only | `duplicate-claim` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `vacuous-check` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `test-capitulation` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `self-consistent-oracle` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `comment-defended-workaround` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `scope-expansion` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `asserted-followup-without-artifact` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `fail-open-default` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| single-review | `rule-applied-in-one-place-not-sibling` | 2 | 2 / 0 / 0 | 2 / 0 / 0 |
| full-adversarial | `vacuous-check` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `test-capitulation` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `self-consistent-oracle` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `comment-defended-workaround` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `scope-expansion` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `asserted-followup-without-artifact` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `fail-open-default` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| full-adversarial | `rule-applied-in-one-place-not-sibling` | 1 | 1 / 0 / 0 | 1 / 0 / 0 |
| **Total** | | **32** | **32 / 0 / 0** | **32 / 0 / 0** |

### Round 4 latency by tier

Times are summed reviewer-invocation wall time. Mandatory checks completed below the timer's one-millisecond resolution and contribute 0 ms in the captures.

| Policy | Corpus tier | Reviewer invocations | Timeouts | Total | Mean | Range |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | single-review | 8 | 8 | 240.264 s | 30.033 s | 30.031 to 30.034 s |
| Unconditioned | full-adversarial | 16 | 16 | 480.543 s | 30.034 s | 30.031 to 30.040 s |
| Conditioned | single-review | 16 | 16 | 480.604 s | 30.038 s | 30.032 to 30.046 s |
| Conditioned | full-adversarial | 16 | 16 | 480.641 s | 30.040 s | 30.035 to 30.056 s |

## Round 3 baseline result

This campaign does not demonstrate that NoSlop is better than other reviewers. Both policies found 10 of 32 expected findings, missed 22, and emitted no unmatched findings. Provenance conditioning added eight reviewer invocations and 240.199 seconds of summed invocation time without finding anything extra.

The split is important: deterministic checks found all 10 cases they can evaluate, while the model-backed reviewer found none of the 22 remaining expectations because every reviewer invocation reached the fixed 30-second timeout.

| Policy | Found | Missed | False-positive | Recall | Reviewer timeouts | Summed invocation time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | 10 | 22 | 0 | 31.25% | 24 / 24 | 720.923 s |
| Conditioned | 10 | 22 | 0 | 31.25% | 32 / 32 | 961.122 s |
| Conditioned minus unconditioned | 0 | 0 | 0 | 0 points | +8 | +240.199 s |

`False-positive` means an emitted finding that did not match an expectation by lens, path, and line. The 32-case corpus scored here had no clean negative-control case, so zero unmatched findings is not a false-positive rate and should not be presented as one.

## Corpus and protocol

The public corpus contains 36 synthetic diffs:

- 27 positive taxonomy cases, three for each of the nine lenses
- 4 credential or identity leak cases
- 4 stale outbound-prose cases using recorded thread-state fixtures
- 1 clean negative constraint-comment case
- 16 `single-review`, 8 `full-adversarial`, and 12 `leak-scan-only` cases

Every `case.json` expectation was written from the case design before either policy ran. Round 5's capture-time expected-finding files had aggregate SHA-256 `c0e10a220825260a27652d9cf338fc4424199194c5a69e4ab91769f3e104a88c`; review later clarified one diagnostic without changing its match key, and the current aggregate is `c8ffbabc7fefc4632deca4977c1685fa6022966a76018b8dc3b6f54e06c84639`. Its campaign manifest is `da1cdba971a975d32273feebfd06d216d9b508cea57b9c57371e8766c0f301a9`. The clean negative declares an explicit empty expectation array rather than omitting expectations.

The unconditioned policy applies the complete taxonomy with no history-selected priority. It uses one reviewer round for `single-review` and two linked rounds for `full-adversarial`. The conditioned policy supplies the case lens as repeated provenance, reviews it first, and raises `single-review` to two linked rounds. `full-adversarial` remains two rounds. The baseline policy files had aggregate SHA-256 `38ea6194194aef6e3a7e5cba755766408df1840e88b6022d61615fa99f3cb9d0`. Round 4 sharpened case-driven reviewer guidance without changing the policy shape; those policy files had aggregate SHA-256 `fa6a54fb8aa653fa5aefe6f8dd5065cc3bdfd85dd9e4012cafff43d89d05c06f`. Round 5 added the `redundant-comment` lens line to both policy files; the capture policy files had aggregate SHA-256 `b6e14aaf4c8d4222e5869c84715be863308bfaa31b6458bf7bdaf9b1201d120f`. Review subsequently clarified that naming a declaration is conventional and only comments adding no other information are redundant. The current policy files, hashed as `cat corpus/policies/*.md | shasum -a 256`, have aggregate SHA-256 `3f6d00c88001c8edcd21849e8c59e132d494ec89822cd71d1936130f864a253c`.

The four baseline and round 4 captures used `opencode/deepseek-v4-flash-free` through OpenCode 1.1.34, sequential execution, no retries, and a 30-second timeout per reviewer invocation. Round 5 used the same model, client, execution order, and retry policy with a one-second timeout per reviewer invocation. A timeout records an empty response. Baseline deterministic leak, test-count, and prose checks replay directly from the diff and recorded thread fixture without reading the expectation. Round 4 adds shared deterministic lens pre-checks driven by the recorded diff and campaign intent, still without reading an expectation.

The test-count harness initially omitted its mandatory probe. That harness defect was caught after the first captures. The probe was added test-first, and the deterministic portion of both captures was refreshed without retrying or replacing any reviewer timeout. No diff, expectation, policy, reviewer response, or timeout was changed.

## Round 3 baseline results by lens and tier

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

## Round 3 baseline latency by tier

Times below are summed invocation wall time. Packets group cases by lens, so the invocation count is the useful denominator. Mandatory checks completed below the timer's one-millisecond resolution and contribute 0 ms in the capture.

| Policy | Corpus tier | Reviewer invocations | Timeouts | Total | Mean | Range |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Unconditioned | single-review | 8 | 8 | 240.306 s | 30.038 s | 30.036 to 30.040 s |
| Unconditioned | full-adversarial | 16 | 16 | 480.617 s | 30.038 s | 30.036 to 30.043 s |
| Conditioned | single-review | 16 | 16 | 480.560 s | 30.035 s | 30.032 to 30.040 s |
| Conditioned | full-adversarial | 16 | 16 | 480.562 s | 30.035 s | 30.032 to 30.039 s |

## Failures and limitations

- All 56 reviewer invocations in the baseline and all 56 in round 4 timed out on the 30-second timeout. Round 5 deliberately used a one-second timeout for a deterministic change, so all 56 of its invocations timed out as well. These campaigns measure the complete configured policy, including endpoint availability, but cannot separate model quality from provider availability. The reviewer-guidance improvement is therefore unmeasured, and no round scores a live reviewer response.
- The corpus is synthetic. It is replayable and expectation-first, but it does not establish performance on naturally occurring pull requests.
- Round 5's 100% recall is a regression-suite result for conservative syntax patterns. It does not show that the pre-checks cover every semantic form of the nine lenses.
- There is one expected finding per case. Multi-defect recall is unmeasured.
- There is one clean negative corpus case. It measures the named genuine-constraint shape, but is far too small to establish a general precision or false-positive rate.
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

Score the baseline captures:

```sh
./bin/noslop evaluate \
  --corpus corpus/seeds \
  --case-set corpus/case-sets/rounds-1-through-4.json \
  --unconditioned-results corpus/results/2026-08-12/unconditioned.json \
  --conditioned-results corpus/results/2026-08-12/conditioned.json
```

Score the round 4 captures:

```sh
./bin/noslop evaluate \
  --corpus corpus/seeds \
  --case-set corpus/case-sets/rounds-1-through-4.json \
  --unconditioned-results corpus/results/2026-08-12-r4/unconditioned.json \
  --conditioned-results corpus/results/2026-08-12-r4/conditioned.json
```

Score the round 5 captures:

```sh
./bin/noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results corpus/results/2026-08-12-r5/unconditioned.json \
  --conditioned-results corpus/results/2026-08-12-r5/conditioned.json
```

The checked-in scorer captures are under `corpus/results/2026-08-12/` for the baseline, `corpus/results/2026-08-12-r4/` for round 4, and `corpus/results/2026-08-12-r5/` for round 5. The first two use the checked-in 32-case manifest, file SHA-256 `b8c806dcd7314e7e3af5b174d628b2cd1e2d5cf6e0c0f783bd234616b1b577c7`, because the live corpus later expanded to 36 cases. The manifest verifies the selected `case.json` and `change.diff` content against framed aggregate SHA-256 `d89a8acb93f3843f28458f34dba2db8d56f05d7908d9a91db5a1b1fb1d8f6aec`; changed or missing snapshot content fails scoring. Their matching `.run.json` files retain policy name, model, timeout, case aliases, tier, priority lens, round, raw response, error, and elapsed milliseconds for every invocation.

Baseline result SHA-256 values:

| File | SHA-256 |
| --- | --- |
| `conditioned.json` | `987e6c7b931297a264098a0c108e90ac7a20586bc59d71a756142c702b61c8ad` |
| `conditioned.run.json` | `d1fd6371259e88cceaaaee747707b57e1a03e85396060b6525781080ca209fd9` |
| `unconditioned.json` | `c3e3d068aa30d9f2012201a21c59b35c45f5fb3917711b7de2af1156fbce06e8` |
| `unconditioned.run.json` | `12e10bb341253d768c7b11a2c3a7aa3baadeb618f09a654d4c5465e3ade3fbb8` |

Round 4 result SHA-256 values:

| File | SHA-256 |
| --- | --- |
| `conditioned.json` | `80e048ffc9cf87ec5aec23cea8b9c5e4cc6bb3e9b8f0b747557a520f01edfd77` |
| `conditioned.run.json` | `e76ac5160d808ecb7fef159ee8c56ca4a21b2ae80f23880316dad1d4eab6b378` |
| `unconditioned.json` | `d05cba6908341a979036f36bc1e397aa346f36ea8774e5e85579fd78c7a3e76d` |
| `unconditioned.run.json` | `6d8e1de43775bb573dab3818995fb87c78e79ea7a07cdc5ebf4ef40c1a56e939` |

Round 5 result SHA-256 values:

| File | SHA-256 |
| --- | --- |
| `conditioned.json` | `67215dfc8a0326e2a2fd91f46e490835c53caf0354c55832d128694e90be5de0` |
| `conditioned.run.json` | `4783cfdd356b37b447d8afc45723daaf5dec0e21e980e457612c927b426cbfde` |
| `unconditioned.json` | `30655608434d96a63e3081aebc4375138c0725fd6b3bf38480290423703137a8` |
| `unconditioned.run.json` | `e75720eb96333ea5cd0d7089a9e625c156060ce7c707ff986fb6ce24e6928627` |

These hashes identify the exact checked-in captures. Re-running the campaign produces a new dated observation and must not overwrite any snapshot.
