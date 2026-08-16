# NoSlop

NoSlop is the reviewer that knows the author is an AI.

Most review tools evaluate a diff as if a person wrote it. AI-authored changes have additional failure modes: checks that prove nothing, tests weakened to fit an implementation, expected values copied from production logic, permissive defaults after an unknown result, and fixes applied to one path but not its siblings.

NoSlop makes those patterns a first-class review contract. It classifies the change before spending reviewer time, applies named AI-authorship lenses, and uses artifact-specific checks for code and outbound prose.

When generating-agent provenance is supplied, NoSlop also conditions the policy on the last 10 changes from that lane and model. Repeated accepted findings can raise the tier, move affected lenses first, and enable mapped deterministic probes. Every decision prints the history rationale. Missing history keeps the v1 route and says so; unreadable history selects `full-adversarial`.

This repository is derived from [kunchenguid/no-mistakes](https://github.com/kunchenguid/no-mistakes) under the MIT License. The pipeline command is now canonically `no-slop`; `no-mistakes` remains a compatibility alias. The separate `noslop gate` policy engine can run before that pipeline or on its own.

## Three pillars

### Risk-proportional depth

Every gate starts by scoring:

- Blast radius: what the changed files can reach at runtime.
- Novelty: new logic, changed logic, mechanical work, or documentation.
- Reversibility: whether a revert is enough to contain the result.

The selected tier and all three reasons print before validation continues. Use `--tier` to override the result. When the override changes the tier, the output records both the original and overridden tier.

| Tier | Work |
| --- | --- |
| `leak-scan-only` | Mandatory leak and identity checks, the configured test-count floor, plus any applicable artifact oracle |
| `single-review` | Mandatory checks and one reviewer pass through every slop lens |
| `full-adversarial` | Mandatory checks, a lens review, an adversarial challenge round, the test-count floor, and the configured test command |

A Markdown-only diff routes to `leak-scan-only` unless it is an agent instruction file, matches a configured high-risk path, or the operator overrides it. Rewriting `AGENTS.md`, `CLAUDE.md`, a `SKILL.md`, or the repository's own `.no-mistakes.yaml` is high risk with no configuration at all, because in an agent repository those files are the runtime. Substantial source additions also reach the full tier even on a feature branch, counted across the whole change rather than only in newly created files.

### AI-slop lenses

The reviewer receives nine named lenses:

- `vacuous-check`
- `test-capitulation`
- `self-consistent-oracle`
- `comment-defended-workaround`
- `scope-expansion`
- `asserted-followup-without-artifact`
- `fail-open-default`
- `rule-applied-in-one-place-not-sibling`
- `redundant-comment`

Every finding carries its lens name. [The taxonomy](docs/src/content/docs/reference/slop-taxonomy.md) defines the failure, reviewer guidance, and available mechanical pre-check for each lens.

### Artifact-class oracles

Secrets and private identity markers are scanned at every tier. The scanner recognizes common credential shapes, personal home paths, and private names from a local blocklist. A missing built-in default blocklist means no private-name list; an explicitly configured missing file and any unreadable file stop evaluation. Findings identify the file and line without copying the matched value. Every honored `noslop:allow-leak` marker prints its file and line and counts in the verdict. Set `slop.leak_scan.allow_exemptions: false` when CI must reject all inline exemptions.

Outbound text can be selected by a configured path or `outbound: true` front matter. The prose oracle checks AI-tell vocabulary, em dashes, cited JSON or CSV numbers, and optional live GitHub issue or pull request state. With `--thread`, it uses `gh` to verify the thread is open and checks whether an existing comment already makes substantially the same claim. An explicit thread with no outbound artifact is an evaluation error.

## Build

NoSlop requires Go 1.25 or newer.

```sh
git clone https://github.com/Blakeolson21/no-slop.git
cd no-slop
go build -o ./bin/noslop ./cmd/noslop
```

Build the pipeline CLI separately:

```sh
go build -o ./bin/no-slop ./cmd/no-slop
```

## Run

Review committed changes against the merge base of the default branch:

```sh
./bin/noslop gate
```

Name the comparison explicitly:

```sh
./bin/noslop gate --base origin/main --head HEAD
```

Supply the requested scope when the gate should mechanically compare new files and reviewer findings with intent:

```sh
./bin/noslop gate --base origin/main --intent "Add the no-store response header only."
```

Override validation depth:

```sh
./bin/noslop gate --base origin/main --tier full-adversarial
```

If provenance raises the tier, a lower `--tier` is refused unless `--force-tier` is also present. The output prints both the provenance signal and the forced override.

Check outbound text against a live GitHub thread:

```sh
./bin/noslop gate --base origin/main --thread https://github.com/owner/repo/issues/123
```

Capture generating-agent provenance for conditioning and later evaluation:

```sh
./bin/noslop gate --base origin/main \
  --provider example-provider \
  --model example-model \
  --reasoning-effort high \
  --lane-id review-lane-1 \
  --change-class source
```

Because the caller supplies `--lane-id` and `--model`, provenance conditioning is advisory until a trusted external system supplies and enforces those values.

Use a different private-name blocklist:

```sh
./bin/noslop gate --base origin/main --blocklist .private-names
```

Exit code `0` means pass, `1` means findings blocked the gate, and `2` means the gate could not evaluate the change.

## Configure

NoSlop uses the existing `.no-slop.yaml` repository config shape:

```yaml
slop:
  data_dir: ".noslop-data"
  leak_scan:
    allow_exemptions: false
  test_command: "go test -race ./..."
```

Keep the real blocklist private and uncommitted. The [repository config reference](docs/src/content/docs/reference/repo-config.md#slop) owns all fields, defaults, outbound selection, and blocklist details.

Replay captured policy findings against the seed corpus:

```sh
./bin/noslop evaluate \
  --corpus corpus/seeds \
  --unconditioned-results results/unconditioned.json \
  --conditioned-results results/conditioned.json
```

The [corpus format](docs/src/content/docs/reference/evaluation-corpus.md) records diffs and independent expected findings. The runner labels the seed corpus and result files as replayed inputs, then reports found, missed, and false-positive counts without inventing reviewer output.

The first [measured 32-case campaign](docs/src/content/docs/reference/evaluation-campaign.md) found 10 expectations, missed 22, and emitted no unmatched findings under both policies. All model-backed reviewer invocations timed out, so the result does not support a superiority claim. Raw captures and latency records are checked in for replay and inspection.

## Development

```sh
make build
go test ./internal/slop/...
go test -race ./...
make lint
go build -o ./bin/noslop ./cmd/noslop
```

## License and credit

MIT licensed. The gate foundation is derived from [no-mistakes](https://github.com/kunchenguid/no-mistakes) by Kun Chen.
