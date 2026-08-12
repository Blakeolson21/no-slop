# NoSlop

NoSlop is the reviewer that knows the author is an AI.

Most review tools evaluate a diff as if a person wrote it. AI-authored changes have additional failure modes: checks that prove nothing, tests weakened to fit an implementation, expected values copied from production logic, permissive defaults after an unknown result, and fixes applied to one path but not its siblings.

NoSlop makes those patterns a first-class review contract. It classifies the change before spending reviewer time, applies named AI-authorship lenses, and uses artifact-specific checks for code and outbound prose.

This repository is an MIT-licensed fork of [kunchenguid/no-mistakes](https://github.com/kunchenguid/no-mistakes). The inherited `no-mistakes` gate remains available. The new `noslop gate` command is a front stage that can run before it or on its own.

## Three pillars

### Risk-proportional depth

Every gate starts by scoring:

- Blast radius: what the changed files can reach at runtime.
- Novelty: new logic, changed logic, mechanical work, or documentation.
- Reversibility: whether a revert is enough to contain the result.

The selected tier and all three reasons print before validation continues. Use `--tier` to override the result. The output records both the original and overridden tier.

| Tier | Work |
| --- | --- |
| `leak-scan-only` | Mandatory leak and identity checks, plus any applicable artifact oracle |
| `single-review` | Mandatory checks, the test-count floor, and one reviewer pass through every slop lens |
| `full-adversarial` | Mandatory checks, a lens review, an adversarial challenge round, the test-count floor, and the configured test command |

A Markdown-only diff always routes to `leak-scan-only` unless the operator overrides it.

### AI-slop lenses

The reviewer receives eight named lenses:

- `vacuous-check`
- `test-capitulation`
- `self-consistent-oracle`
- `comment-defended-workaround`
- `scope-expansion`
- `asserted-followup-without-artifact`
- `fail-open-default`
- `rule-applied-in-one-place-not-sibling`

Every finding carries its lens name. [The taxonomy](docs/taxonomy.md) defines the failure, reviewer guidance, and available mechanical pre-check for each lens.

### Artifact-class oracles

Secrets and private identity markers are scanned at every tier. The scanner recognizes common credential shapes, personal home paths, and private names from a local blocklist. Findings identify the file and line without copying the matched value.

Outbound text can be selected by a configured path or `outbound: true` front matter. The prose oracle checks AI-tell vocabulary, em dashes, cited JSON or CSV numbers, and optional live GitHub issue or pull request state. With `--thread`, it uses `gh` to verify the thread is open and checks whether an existing comment already makes substantially the same claim.

## Build

NoSlop requires Go 1.25 or newer.

```sh
git clone https://github.com/Blakeolson21/no-slop.git
cd no-slop
go build -o ./bin/noslop ./cmd/noslop
```

The inherited gate can still be built separately:

```sh
go build -o ./bin/no-mistakes ./cmd/no-mistakes
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

Override validation depth:

```sh
./bin/noslop gate --base origin/main --tier full-adversarial
```

Check outbound text against a live GitHub thread:

```sh
./bin/noslop gate --base origin/main --thread https://github.com/owner/repo/issues/123
```

Use a different private-name blocklist:

```sh
./bin/noslop gate --base origin/main --blocklist .private-names
```

Exit code `0` means pass, `1` means findings blocked the gate, and `2` means the gate could not evaluate the change.

## Configure

NoSlop uses the existing `.no-mistakes.yaml` repository config shape:

```yaml
slop:
  leak_scan:
    blocklist_file: ".noslop-blocklist"
  test_command: "go test -race ./..."
```

Keep the real blocklist private and uncommitted. The [repository config reference](docs/src/content/docs/reference/repo-config.md#slop) owns all fields, defaults, outbound selection, and blocklist details.

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
