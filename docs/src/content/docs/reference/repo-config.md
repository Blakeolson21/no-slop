---
title: Repo Config Reference
description: All fields for .no-slop.yaml.
---

Per-repo configuration lives in `.no-slop.yaml` at the root of your repository.
`.no-mistakes.yaml` remains a compatibility filename during the rename. If both
files are present in the same tree, no-slop parses both and refuses that tree
unless their resolved settings are semantically equal. New changes should keep
only `.no-slop.yaml`.

:::caution[Security: gate-control fields are read from the default branch]
`commands.*` execute arbitrary shell on the daemon host via `sh -c` / `cmd.exe /c`, and `agent` selects which process launches there (including ordered fallback lists, ACP aliases such as `cursor`, and `acp:` targets) with the maintainer's credentials.
To prevent a supply-chain attack where a contributor lands a hostile value on a gated branch, the daemon always reads **`commands` and `agent` from your default branch** (e.g. `origin/main`), never from the pushed SHA, and reads them at the exact commit a fresh fetch resolved (so a stale `origin/<default>` ref cannot serve a value the live default branch removed).
The daemon also reads `document.instructions`, the `review` section (`review.path_instructions`, `review.convergence`), `disable_project_settings`, `no_ci`, and `ci.rerun_transient` only from that trusted copy.
If the default branch cannot be fetched and resolved to a readable commit, or its present repo config (`.no-slop.yaml` or the `.no-mistakes.yaml` compatibility filename) cannot be read and parsed, the run aborts before launching an agent.
A readable default-branch tree with neither config file is valid and uses defaults.
Commit the gate-control settings you want to your default branch.
Non-executing fields (`ignore_patterns`, `auto_fix`, `commit`, `intent`, `test`) are still read from the pushed branch.

If you genuinely want per-branch `commands` and `agent` (for example, a single-developer repo where you trust your own feature branches), opt in with [`allow_repo_commands: true`](#allow_repo_commands) in this same file on your default branch. This re-enables the previous behavior with eyes open. The switch is read only from the trusted default-branch copy, so a contributor cannot self-enable it from a pushed branch.
:::

```yaml
# .no-slop.yaml

agent: codex

commands:
  lint: "golangci-lint run ./..."
  # Targeted local validation only - not a full-repo CI-parity suite.
  test: "go test ./internal/cli -run '^TestDoctor' -count=1"
  format: "gofmt -w ."

ignore_patterns:
  - "*.generated.go"
  - "vendor/**"

# Optional documentation ownership policy, read only from the trusted default branch.
document:
  instructions: |
    docs/ owns detailed product guidance; README.md owns the introduction.

# Optional extra review guidance, scoped to the paths a change touches.
# Read only from the trusted default branch.
review:
  path_instructions:
    - path: "internal/scm/**"
      instructions: |
        Any URL or error string that can carry credentials must go through internal/safeurl.
    - path: "docs/**"
      instructions: |
        Prose changes only. Do not request test coverage.

# For orchestration repos whose project instructions would misidentify gate agents.
# Read only from the trusted default branch. Defaults to false.
disable_project_settings: true

# Positive declaration that this repository intentionally has no CI.
# Read only from the trusted default branch. Defaults to false (CI expected).
# no_ci: true

auto_fix:
  rebase: 3
  review: 3
  test: 3
  document: 3
  lint: 5
  ci: 3

# Read only from the trusted default branch: each rerun is another workflow run.
ci:
  rerun_transient: 0

commit:
  fix_message: "chore(no-slop-{{.Step}}): {{.Summary}}"

intent:
  enabled: true
  threshold: 0.2
  slack_days: 3
  disabled_readers: []

test:
  evidence:
    store_in_repo: true
    dir: .no-slop/evidence

# NoSlop front-stage settings.
slop:
  data_dir: ".noslop-data"
  provenance_required: false
  base_ref:
    remote: "origin"
    branch: "main"
  risk:
    single_review_threshold: 3
    full_adversarial_threshold: 6
    high_risk_paths:
      - "internal/auth/**"
      - ".github/workflows/**"
  leak_scan:
    blocklist_file: ".noslop-blocklist"
    allow_exemptions: false
  prose:
    outbound_paths:
      - "outbound/**"
    ai_tell_words: []
  test_count_floor: true
  test_command: "go test -race ./..."
```

## Fields

### slop

Configure the `noslop gate` front stage. The classifier always prints its tier and reasons before continuing. The full-adversarial tier runs a lens review, a second adversarial challenge round, and the configured tests. Pass `--intent` when the deterministic scope pre-check and reviewer should compare the change with a stated request; without an intent, or with one that states no scope limit the detector can act on, the scope pre-check reports itself as not armed on the mandatory-check line rather than contributing a silent zero.

**`--tier` is escalate-only.** It may raise the computed tier and prints `override raised: <computed> -> <requested>`. Any request to lower it, including with `--force-tier`, is refused with exit 2 and prints `override refused: <computed> -> <requested>`; the error names the computed tier. `--force-tier` is still accepted so existing invocations do not become argument errors, and it can no longer buy a cheaper tier. The reason is that the caller of this command and the author of the change under test are routinely the same agent, and a single flag that lowered the tier carried both an authorization weakening and a fleet-instruction rewrite to a passing verdict at exit 0. If a repository genuinely needs a cheaper route, raise `slop.risk.single_review_threshold` and `slop.risk.full_adversarial_threshold` in the repository config, which is read from the base ref and is therefore outside the reach of the change being gated.

**Every `slop.*` value is read from the base ref, not from the working tree.** A gate whose strictness is configured by the artifact being gated is not a gate, so the whole block is resolved from the repo config as it exists at the base revision, reading `.no-slop.yaml` and the `.no-mistakes.yaml` compatibility name through the same alias rules the worktree load applies, exactly as the daemon already treats these fields for a pushed branch. A value absent at the base ref means the built-in default; a base copy that is present but unparsable stops the run rather than falling back to a default. When the head worktree sets any of these fields differently, the base value is the one in force and the difference is reported as a `gate-config-drift` finding, so a change that edits the gate's own controls is itself flagged. An uncommitted repo config therefore changes nothing.

**Change the gate's configuration in its own commit, and land it before the change that relies on it.** `gate-config-drift` is a blocking finding in both directions, including a change that makes the gate stricter, and that is deliberate rather than an oversight. A config change cannot certify itself: the run that would bless it is the run it is trying to reconfigure, and deciding whether an edit tightens or loosens the gate requires the gate to judge its own controls, which is the authority being changed. Direction is also not a property this file can read generically, since a lower threshold is stricter while a longer `high_risk_paths` list is stricter too. So the flow is two steps. Put the `slop.*` edit on the base branch on its own, let it land, and then open the change that depends on the new values, which now sees no drift because the base already carries them. The finding names this: it prints the drifted field, the head and base values, and `land the slop.* change on the base branch first, because a config change cannot certify itself`.

**Which commit is the base ref does not come from anything the author can write.** There is no `--base` flag, and no local ref participates in resolving the canonical commit. Both of those are removals rather than restrictions, because restricting them is what failed: an earlier version validated `--base` against a canonical ref resolved by rev-parsing the string `origin/main`, and `git rev-parse` searches `refs/`, `refs/tags/`, `refs/heads/`, and `refs/remotes/` in order, so `git branch origin/main`, `git tag origin/main`, `git update-ref refs/remotes/origin/main`, and `git fetch . +<sha>:refs/remotes/origin/main` each made an author-owned commit the base with a single command.

The base is resolved in exactly three steps:

1. **An orchestrating pipeline** may supply the base directly through the Go API (`slopcli.Options.PipelineBase`). That channel requires being in-process with the gate: no flag, file, or ref reaches it.
2. **The network.** `git ls-remote <configured remote URL> refs/heads/<branch>` is asked what the canonical branch points at, and the base is `merge-base(HEAD, <the commit the remote answered with>)`. The branch is `slop.base_ref.branch` when pinned and `main` then `master` otherwise; the remote is `slop.base_ref.remote` or `origin`. The full refname is used, so a tag on the remote cannot answer for a branch. If the remote's commit is not in the local object store the run refuses and says to fetch, rather than guessing a nearer commit.
3. **Nothing.** Offline, with no remote, or with a remote that will not answer, the run is pinned to `full-adversarial`, reads built-in defaults instead of a base config it cannot trust, reports a `base-ref-unverified` finding, and fails. It never lowers.

A repository whose trunk is neither `main` nor `master` names it in `slop.base_ref`, and that name has to already be committed to the ref it points at, which is now a ref on the remote rather than any local branch. The pin is read from the config at the provisionally resolved base, so moving the canonical ref is authorized by the previous canonical ref rather than by the change proposing the move. Every run prints the base commit and how it was verified, and the three routes print differently: an unverified run cannot be mistaken for a verified one.

The residual is that the remote's URL comes from `.git/config`, which is local. An author who repoints `origin` at a repository they control can still make that repository's answer canonical. That is a materially louder act than creating a ref, and `slop.base_ref.remote` is read from the base config, so which remote is asked is itself an operator decision.

`--blocklist` adds names to the configured private-name list rather than replacing it, so the command line cannot point the identity scan at an empty file.

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `slop.data_dir` | `string` | `.noslop-data` | Repository-relative or absolute directory for append-only provenance history |
| `slop.base_ref.remote` | `string` | Empty | Remote whose copy of the canonical branch the base is resolved against |
| `slop.base_ref.branch` | `string` | Empty | Canonical branch the base must sit on; empty means detect the default branch |
| `slop.provenance_required` | `bool` | `false` | Refuse the run when the provenance history file is absent |
| `slop.risk.single_review_threshold` | `int` | `3` | Minimum sum of the three risk axes for `single-review` |
| `slop.risk.full_adversarial_threshold` | `int` | `6` | Minimum sum for `full-adversarial`; must be greater than the single-review threshold |
| `slop.risk.high_risk_paths` | `string[]` | Empty | Extra glob patterns treated as high-reach paths, on top of the built-in markers |
| `slop.leak_scan.blocklist_file` | `string` | `.noslop-blocklist` | Repository-relative or absolute private-name file |
| `slop.leak_scan.allow_exemptions` | `bool` | `false` | Honor and report inline `noslop:allow-leak` markers; off by default so a change cannot exempt its own credential |
| `slop.prose.outbound_paths` | `string[]` | `outbound/**` | Paths whose changed Markdown is intended for publication |
| `slop.prose.ai_tell_words` | `string[]` | Empty | Additional case-insensitive vocabulary to flag |
| `slop.test_count_floor` | `bool` | `true` | Block when recognizable tests in changed files fall below the base revision |
| `slop.test_command` | `string` | Empty | Test command required by the `full-adversarial` tier |

`slop.test_command` is deliberately separate from `commands.test`. The inherited pipeline uses `commands.test` for focused local validation. NoSlop's full tier uses `slop.test_command` as the explicit adversarial test gate.

The provenance history is append-only in both directions. A record is never rewritten, and the read side keeps the WORST record any run of a change produced rather than the latest one, so re-running the same command cannot replace an incriminating record with a clean one.

**Retention is by age and severity, and there is deliberately no count in it.** Every count-shaped rule this store has had turned out to be an eviction tool, because the audited party mints the thing being counted: a tail of the last N records was cleared by re-running one change, and a window of the last 10 distinct *changes* was cleared by ten `git commit --amend` cycles of one trivial edit, which mint ten head SHAs for byte-identical content in seconds. A record is now retained while it is younger than the 90-day retention window, and a record carrying accepted findings is retained regardless of age. Rotation therefore cannot remove an incriminating record at any price.

**An escalation clears only by a reviewed pass:** a record whose change actually ran the `full-adversarial` protocol, completed its review rounds, passed, and carried no accepted finding for that lens. Nothing else resets a lens score.

State the guarantee precisely rather than as "an escalation cannot be cleared", because the store is a file on the same filesystem as the change being judged. What holds is narrower:

- Deleting or truncating the history is **caught, not prevented**. A monotonic high-water count sidecar makes a history shorter than the number of records already accepted read as unreadable, which escalates the run to `full-adversarial` rather than resetting to a first-time lane. Deleting the history and the sidecar together still resets the store.
- Re-running, replaying, or amending a change **cannot evict anything**. That part is structural.
- Hand-writing a plausible record into the file is **not prevented at all**. Every field a forger would need is held there in the clear, including the fields that make a record read as a reviewed pass.

Closing the last one needs a history the audited party cannot write, which is a service rather than a file. `slop.provenance_required` refuses a run whose store went missing, and pointing `slop.data_dir` at a directory the audited party cannot write is the operator-side answer available today. The `--lane-id` and `--model` values are self-asserted by the caller and nothing authenticates them, so every run states that in its header.

Each completed `noslop gate` appends a schema-versioned JSON Lines record under `slop.data_dir`. The record includes provider, model, reasoning effort, agent lane identifier, change class, selected tier, accepted and rejected findings by lens, review rounds, fix growth, and outcome. Use `--provider`, `--model`, `--reasoning-effort`, `--lane-id`, and `--change-class` to identify the generating lane. Missing values are recorded as `unknown` and do not select lane/model history.

For a known lane and model, the classifier reads the retained window described above. Three accepted findings for one lens raise the selected tier by one level, move repeated lenses to the front of the review prompt, and enable an available deterministic probe. Rejections are recorded but never subtracted, because conditioning may only raise a tier and subtracting let a forged record cancel a real one. `test-capitulation` enables `test-count-floor`. No matching history keeps the v1 route. Unreadable or malformed configured history selects `full-adversarial`. The selected rationale, lens priority, and probes print with the normal axis decision.

**Every identity path costs the same, because none of them is verifiable.** On a store that already holds records naming a lane and model, an omitted `--lane-id` or `--model`, an `unknown` value, and a self-asserted pair the store has never seen all select `full-adversarial`. Punishing only omission was backwards: `--lane-id lane-zzz` cleared an escalation that saying nothing could not, so lying was strictly cheaper than silence. A store with no identified records keeps the v1 route on all of those paths, so a repository that never opted into lane identity pays nothing.

Because the caller supplies `--lane-id` and `--model`, provenance conditioning is advisory until a trusted external system supplies and enforces those values. Choosing a new lane value does not buy a cheaper route, because a first-contact key escalates exactly like an omitted one, but it does prevent prior lane history from matching. The resolved store location prints on every run.

Provenance writes are append-only and serialized across concurrent gate processes. Keep the default local data directory out of version control. A later run reads only records whose agent lane identifier and model match exactly.

The private-name file accepts one literal entry per line. Blank lines and lines beginning with `#` are ignored. Matching is case-insensitive. Keep real hostnames, codenames, project names, and other private identities out of the repository. If the built-in default `.noslop-blocklist` is absent, the gate prints that it is scanning without a private-name list and continues with built-in credential patterns. A path selected in repository config or with `--blocklist` is an operator commitment, so a missing configured path stops the gate. An unreadable file at either the default or configured path also stops the gate.

Inline exemptions are **off by default**. A mandatory check the audited party turns off for itself with a comment on the same line is a mandatory check by name only, and with the marker honored out of the box a change could exempt its own credential and pass at exit 0 in a repository with no config at all. With exemptions off, every marker becomes a blocking finding naming the line.

Set `slop.leak_scan.allow_exemptions: true` at the base ref when a repository genuinely holds credential-shaped fixtures. Then `noslop:allow-leak` on the same source line exempts that line only. The marker must open a comment or the line: it has to be the first thing on the line, or follow `//`, `#`, `/*`, `<!--`, `--`, or `;`. A marker sitting inside a sentence exempts nothing, so documentation that quotes it does not suppress a credential on its own line. Every honored marker prints as `file:line` with the number of findings it actually suppressed, and the summary reports both the marker count and the suppressed count, because a marker on a clean line is not a bypass.

Markdown can opt into the prose oracle without a matching path by adding front matter:

```yaml
---
outbound: true
---
```

When `noslop gate --thread <url>` is provided, NoSlop requires at least one outbound artifact and calls `gh` to confirm that the target GitHub issue or pull request is open and to compare the draft with existing comments. No outbound artifact, an unavailable thread, or an unreadable thread fails the command closed.

When an outbound line cites a repository-relative `.json` or `.csv` file and states a number, NoSlop checks that the number appears in or can be derived from the file's numeric values. Supported derivations include totals, counts, averages, minima, maxima, ratios between two named fields, and outcome percentages derived from named pass, fail, or skip fields. Every derivation is computed over the fields the sentence names, when it names any, so a total that cites two fields must be the total of those two. A ratio binds numerator and denominator to the order the sentence names them. Direct claims against named evidence must name the matching field, and a cited file whose numbers carry no usable names supports no claim at all.

The default thresholds let high-risk changes reach `full-adversarial` on a feature branch. A substantial source addition also reaches it: at least 500 added source lines, or at least 40 net new declarations, counted across the whole change rather than only in newly created files. Both halves matter, because keying on file creation let an author drop two tiers by pasting into an existing file, and physical line count alone missed a generated file holding thousands of declarations on a few hundred lines. Smaller ordinary source changes remain eligible for `single-review`.

The following paths are high risk with no configuration at all, because rewriting one of them changes what the next agent is permitted to do: `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `SKILL.md`, `.cursorrules`, `copilot-instructions.md` and their siblings at any depth, the `.claude/`, `.codex/`, `.cursor/`, `skills/`, and `prompts/` trees, and the repo config and `.gitattributes` files that decide how strictly this gate runs and how git renders the diff every mechanical check reads.

Glob patterns accept `**` as a multi-segment wildcard at any position, so `**/AGENTS.md` matches the file at any depth; `*` never crosses a path separator. A configured pattern that matches no path at head prints a warning, because a protection that silently covers nothing is indistinguishable from one that works.

#### Running the gate from a pipeline

A CI job or an agent harness invokes the gate the same way a person does, and the base-ref rules above are what make that safe when the job runs on the author's own branch.

- Fetch the canonical branch before the run. The gate asks the remote which commit it points at and then requires that commit in the local object store; a checkout that does not carry it refuses and says to fetch, rather than guessing a nearer commit.
- Run with no base argument. There is no `--base` flag: passing one exits 2 and explains why.
- An in-process orchestrator that already resolved the base passes it through `slopcli.Options.PipelineBase` rather than through the command line. That channel is unreachable from a flag, a file, or a ref, which is what makes it safe when a flag was not.
- Set `slop.base_ref.remote` and `slop.base_ref.branch` when the canonical branch is not the detected default, for example a long-lived release branch. Set them in the repository config, not on the command line; there is deliberately no flag.
- Expect a run with no reachable remote to fail. It is pinned to `full-adversarial`, ignores the base config, and reports `base-ref-unverified`. That is the design: a gate that cannot tell which history is the operator's certifies nothing.
- Pass `--lane-id` and `--model` so provenance history accumulates per generating lane. Both are self-asserted, and the run header says so. Point `slop.data_dir` at a path the job persists between runs, or the history is a fresh store every time.
- Read the exit code, not the log: 0 is a pass, 1 is a verdict failure, and 2 is the gate refusing to validate at all, which includes a refused tier request, a `--base` argument, an unusable base, and an unavailable reviewer.
- A submodule pointer bump is a `notice`, not a failure: its content is in another repository and no check here will ever see it, so the tier is raised for review instead of the run being refused. `notice:` lines are reported and do not block; `finding:` lines block.

The complete lens definitions are in the [NoSlop taxonomy](./slop-taxonomy/).

The [evaluation corpus reference](./evaluation-corpus/) owns the replay format. `noslop evaluate` compares captured conditioned and unconditioned findings against the recorded expectations and reports found, missed, and false-positive counts.

### agent

Override the default agent for this repo and its setup-wizard suggestions.

| | |
| --- | --- |
| Type | `string` or `string[]` |
| Values | `auto`, `claude`, `codex`, `rovodev`, `opencode`, `pi`, `copilot`, `cursor`, `acp:<target>` |
| Default | Inherits from global config |

`auto` resolves to the first supported native agent or ACP alias in this order: `claude`, `codex`, `opencode`, `acli` with `rovodev` support, `pi`, `copilot`, then `cursor`.
`cursor` is an ACP alias for the `cursor` target with default command `cursor-agent acp`.
Its availability uses the global `acpx_path` and `acp_registry_overrides.cursor` settings when present.
`acp:<target>` uses the user-installed `acpx` binary configured in global config; `acp:cursor` uses the same default command as `cursor`.
Arbitrary `acp:<target>` agents are opt-in and are not considered by `agent: auto`.
The effective agent configuration must resolve to a runnable runner before a new validation gate starts.
If the selected explicit agent or `auto` is unavailable, the gate fails before its first pipeline step rather than reporting partial validation as passed.

You can also set an ordered fallback list:

```yaml
agent: [codex, claude]
```

The list is filtered to entries available to the daemon at run startup, and the first available entry becomes the primary agent.
After resolving `auto`, entries that resolve to the same ACP target are deduplicated in list order, so `cursor` and `acp:cursor` provide one fallback and preserve whichever spelling appears first.
If no entry is available, the gate fails before its first pipeline step.
If a pipeline invocation fails because that agent process cannot start or exits with an error, no-slop retries that invocation with the next available fallback.
Structured findings and schema/output validation problems do not trigger fallback.
This per-repo `agent` value, including every fallback entry, is still read from the trusted default-branch `.no-slop.yaml` unless `allow_repo_commands` is enabled there.

### allow_repo_commands

Opt in to honoring the code-executing selection fields (`commands.{test,lint,format}` and `agent`) from a contributor's pushed branch instead of the trusted default-branch copy.

| | |
| --- | --- |
| Type | `bool` |
| Default | `false` |

This field is itself read **only from the trusted default-branch copy** of `.no-slop.yaml`, never from the pushed SHA, so a contributor cannot self-enable it by setting it on a feature branch. By default the daemon reads `commands` and `agent` from your default branch (e.g. `origin/main`) so a pushed SHA cannot inject shell or pick the launched agent on the daemon host. This opt-in covers those two fields only; `document.instructions`, `review.path_instructions`, and `disable_project_settings` stay trusted-only either way. Leave this `false` for any repo that accepts contributions. Set it to `true` only for a single-developer environment where you trust every branch you push (for example, a personal repo gated by your own daemon).

### disable_project_settings

Suppress project-level agent settings and instructions for every gate-agent start and resumed session.

| | |
| --- | --- |
| Type | `bool` |
| Default | `false` |

This opt-in is intended for agent-orchestration repositories whose `AGENTS.md`, `CLAUDE.md`, or harness-specific project settings would give a validation agent an operator identity and authority that it must not adopt.
When enabled, no-slop suppresses the target checkout's project settings for every agent-driven gate step while preserving user-level agent configuration.
Codex, Claude, and Pi are the currently verified agents: Codex receives `project_doc_max_bytes=0` and `--ignore-rules`, Claude loads only its user setting source, and Pi runs with `--no-context-files` (preserving a pinned `--no-context-files` or `-nc` spelling).
The setting applies to both new and resumed sessions.

The gate fails before launching an agent if any resolved agent or fallback lacks a verified suppression mechanism.
It also fails if `agent_args_override` defeats suppression, such as a nonzero Codex `project_doc_max_bytes` or Claude setting sources that include `project` or `local`.
When this option is `false`, missing, or `null`, all agents retain their existing project-setting behavior.

This field is honored **only from the trusted default-branch copy** of `.no-slop.yaml`, regardless of `allow_repo_commands`.
A pushed branch cannot enable it or disable a trusted opt-in.
If the trusted commit or its present config file cannot be read and parsed, the run aborts rather than guessing that the option is disabled.

### no_ci

Declare that this repository intentionally has no CI.

| | |
| --- | --- |
| Type | `bool` |
| Default | `false` |

When `true` and the forge reports **zero** checks on the PR head, the CI monitor treats that empty result as all-checks-passed and `axi run` may return `outcome: checks-passed`. The monitor log names the declaration (`no_ci: true`) so the positive evidence stays inspectable rather than silently equating every empty forge response with green.

Absence of this field means CI is expected. A zero-length check result then stays not-ready for as long as the forge reports no checks - elapsed time, grace periods, workflow-file presence or absence, prior check history, and branch names are not evidence.

If checks still appear on a declared no-CI repository, their actual states are processed normally. The declaration never waives a registered pending or failing check.

This field is honored **only from the trusted default-branch copy** of `.no-slop.yaml`, regardless of `allow_repo_commands`.
A feature branch cannot self-declare `no_ci: true` to bypass checks, and cannot clear a trusted declaration either.

### commands.test

Explicit **targeted** local test command. Run via the platform shell - `sh -c` on POSIX, `cmd.exe /c` on Windows.

| | |
| --- | --- |
| Type | `string` |
| Default | Empty (agent selects the smallest relevant tests and evidence checks) |

`commands.test` is local **targeted validation** of the change and requested intent, not a CI-parity repository-wide regression command.
Broad regression belongs in remote CI and remains mandatory before a PR is ready; do not put a complete-suite walk here just to mirror CI.
no-slop does not guess whether an arbitrary shell string is "too broad" - the contract is documented and dogfooded, not enforced with language- or filename-specific heuristics.

When set, the test step runs this exact command first as the baseline and checks the exit code.
When empty, the agent detects and runs the smallest relevant tests itself (and is instructed never to run the complete repository suite).
When user intent is available, the agent may still run after a successful baseline command to gather evidence-oriented validation, still under the same targeted-validation contract.

### commands.lint

Explicit lint command. Run via the platform shell - `sh -c` on POSIX, `cmd.exe /c` on Windows.

| | |
| --- | --- |
| Type | `string` |
| Default | Empty (agent auto-detects) |

When set, the lint step runs this exact command and checks the exit code.
When empty, the agent-driven lint duty is folded into the document step's combined housekeeping pass: one agent invocation covers both documentation and lint, and the lint step consumes that result, reporting lint-category findings with the same gate semantics (blocking findings park for a decision).
Neither responsibility is skipped: when the document step has nothing to run against (or its structured output cannot be trusted), the lint step runs its own agent pass as before.

### commands.format

Formatter command run before the push step commits agent fixes.

| | |
| --- | --- |
| Type | `string` |
| Default | Empty (no separate push-step formatter) |

This does not prevent empty `commands.lint` from detecting and running formatters during the combined housekeeping pass, or during the lint step when that pass cannot provide a result.

### document.instructions

Repository-specific documentation ownership policy for the document step.

| | |
| --- | --- |
| Type | `string` (multiline) |
| Default | Empty (built-in placement policy only) |

The document step always applies a built-in placement policy: every fact has exactly one authoritative owner document, stale duplicates are removed or reduced to pointers instead of synchronized, no new documentation surfaces are created merely to close perceived gaps, and incident lessons live as invariants near their owner (with a pointer to the regression test), never as AGENTS.md postmortems.
`document.instructions` states this repository's ownership map or extra placement rules (for example, which file owns which class of facts).
It augments or clarifies the built-in policy; it cannot disable documentation integrity.

Like `commands.*` and `agent`, this field steers gate behavior, so it is honored **only from the trusted default-branch copy** of `.no-slop.yaml`: a contributor's pushed branch cannot weaken the documentation rules that gate its own review.

### review.path_instructions

Extra review guidance, scoped to the paths a change actually touches.

| | |
|---|---|
| Type | `object[]` with `path` (`string`) and `instructions` (`string`, multiline) |
| Default | Empty (built-in review instructions only) |

Use this for house rules that only apply to part of the tree, for example a redaction rule for the code that builds remote URLs, or a note that a documentation directory needs no test coverage:

```yaml
review:
  path_instructions:
    - path: "internal/scm/**"
      instructions: |
        Any URL or error string that can carry credentials must go through internal/safeurl.
    - path: "docs/**"
      instructions: |
        Prose changes only. Do not request test coverage.
```

Each matched rule reaches the reviewer with the scope it was selected for, so a rule scoped to one directory can never read as a repository-wide instruction:

```
path: docs/**
matched files: docs/notes.md
instructions:
Prose changes only. Do not request test coverage.
```

#### Matching

`path` uses the same matcher and syntax as [`ignore_patterns`](#ignore_patterns), including the rule that `*` never crosses a `/`, so `**/*.go` covers a single directory level rather than every Go file.

The review step appends only the blocks whose `path` matches at least one changed file, in the order they appear in the file.
Two entries with the same `path` **and** the same `instructions` are injected once. The same instruction text under two different `path` values is injected once per path, because each block states its own scope. Two entries with the same `path` and different `instructions` are both injected.
Matching runs against the full changed-file list and is deliberately **not** filtered by `ignore_patterns`: that field is read from the pushed branch, so filtering here would let a contributor drop one of your rules from the review of their own branch.

Blocks augment the built-in review instructions; they cannot disable them, and a finding the reviewer raises from a block goes through the same severity and action model as any other finding.
With nothing configured, or nothing matching the change, the review prompt is exactly what it would be without this setting.
The step log names the rules it applied and the rules that matched nothing, so a rule that never fires is visible in `no-slop axi logs --step review`.

#### Limits and validation

`instructions` is prompt text, so merge-conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) are removed from it and runs of whitespace are collapsed, exactly as for [`document.instructions`](#documentinstructions). Write rules without those tokens; a value that would be left empty once they are removed is rejected rather than silently dropped.

At most 32 entries are allowed, and the assembled prompt section may not exceed 16,384 bytes, because the injected text shares the review prompt's budget and an oversized prompt fails the agent invocation outright.
The size is measured on what is actually injected: the heading, and for every entry its labels, its `path`, its `instructions`, and a 192-byte allowance for its matched-file list. A block whose matched-file list would exceed that allowance is truncated with a `+N more` suffix, so the measured limit holds for any diff.

A missing `path` or `instructions` value, an `instructions` value that renders empty, a `path` that is not a valid glob, or a config over either limit fails when the config is parsed, so the run aborts before an agent starts instead of silently dropping guidance.
These checks run on whichever copy of the file is parsed, including the pushed branch's. A pushed branch's blocks are ignored when the review prompt is built (see [Trust](#trust) below), but an invalid block on that branch still fails its own run, so a broken rule surfaces before it merges and becomes the trusted copy.

#### Trust

Like `document.instructions`, this field steers gate behavior, so it is honored **only from the trusted default-branch copy** of `.no-slop.yaml`, regardless of [`allow_repo_commands`](#allow_repo_commands): a value present only on a pushed branch is ignored, so a contributor cannot inject instructions into the review that gates them.

### review.convergence

Thresholds for the review-loop convergence guard.

| | |
|---|---|
| Type | `object` with `non_decreasing_rounds`, `recurring_rounds`, `budget_minutes` (all `int`) |
| Default | `non_decreasing_rounds: 3`, `recurring_rounds: 3`, `budget_minutes: 120` |

A review-fix loop can *ladder* instead of converge: each fix round relocates a defect or creates new files that the next re-review then flags, so findings per round never shrink, and from the outside every round looks like fresh progress. After every review round the pipeline computes a convergence report from the round history: findings count per round, cumulative review time, findings in files outside the originally submitted diff, and finding classes that recur across rounds under different ids and file paths (identity comes from normalized finding content, so a defect that moves files is still recognized as the same defect).

The gate always carries this report as its `convergence` block, so the history is visible without tallying rounds by hand. The guard trips when any threshold is met:

- `non_decreasing_rounds`: the findings count has not decreased across this many trailing consecutive rounds (the observed ladder was `1,1,1,2,3,3,3`).
- `recurring_rounds`: one finding class has recurred in this many distinct rounds.
- `budget_minutes`: cumulative review execution time (excluding time parked at gates) has reached this budget.

Set a threshold to `0` to disable that trigger; negative values are rejected. A tripped guard is **advisory**: it never aborts the run and never suppresses a finding. It stops `auto_fix.review` from funding further automatic fix rounds, stops `--yes` from auto-answering the gate with `fix`, parks the gate with an explicit convergence warning, and leaves approve/skip/fix available for a deliberate decision.

```yaml
review:
  convergence:
    non_decreasing_rounds: 3
    recurring_rounds: 3
    budget_minutes: 90
```

Like the rest of the `review` section, these thresholds are honored **only from the trusted default-branch copy** of `.no-slop.yaml`: a pushed branch cannot widen or disable the guard on its own run.

### Command process lifetime

All configured `commands.*` entries are scoped to their step.
After no-slop starts one of these commands, it terminates any remaining child processes from that command when the command exits, fails, or the step is cancelled.
Do not rely on a configured command to leave a background server or watcher running after it returns; keep that service inside the command lifetime or start it outside no-slop.

### ignore_patterns

Paths to exclude from review and documentation checks.

| | |
| --- | --- |
| Type | `string[]` |
| Default | Empty (no ignores) |

Pattern matching rules. [`review.path_instructions`](#reviewpath_instructions) uses the same matcher, so there is one path syntax to learn:

| Pattern | Rule |
| --- | --- |
| `*.generated.go` | No slash - matches by basename, at any depth |
| `vendor/**` | Ends with `/**` - matches that directory and everything under it |
| `some/path/file.go` | Contains a slash - full path glob against the whole path |
| `**/*.go` | Also a full path glob, so **only one directory level** - `internal/main.go`, not `internal/scm/github/github.go` |

`*` never crosses a `/`, on every platform, so `**/*.go` is not "every Go file"; it behaves as a single-segment wildcard. Use `*.go` to match by extension at any depth, or `internal/**` to cover a subtree.

### auto_fix

Override auto-fix attempt limits for specific steps. Fields not set here inherit from global config.

| | |
|---|---|
| Type | `object` |

| Field | Type | Default |
| --- | --- | --- |
| `auto_fix.rebase` | `int` | Inherits from global (default `3`) |
| `auto_fix.review` | `int` | Inherits from global (default `0`) |
| `auto_fix.test` | `int` | Inherits from global (default `3`) |
| `auto_fix.document` | `int` | Inherits from global (default `3`) |
| `auto_fix.lint` | `int` | Inherits from global (default `3`) |
| `auto_fix.ci` | `int` | Inherits from global (default `3`) |

Set to `0` to disable the follow-up auto-fix loop for a step (findings require manual approval).
The document step attempts documentation fixes during its initial pass, so unresolved documentation findings pause for approval instead of using an automatic follow-up loop.
For empty `commands.lint`, the document step's combined housekeeping pass also attempts safe lint fixes, and the lint step consumes its result; unresolved blocking lint findings pause for approval instead of starting another automatic fix loop.

`auto_fix.ci` covers the CI step's CI failure and merge-conflict auto-fix attempts.

Legacy alias: `auto_fix.babysit`.

### ci.rerun_transient

How many times the CI step may re-run a single check the provider reported as cancelled before that check reaches an approval gate.

| | |
|---|---|
| Type | `int` |
| Default | `0` |
| Range | `0` to `5`; values outside it are clamped |
| Trust | Read only from the trusted default branch |

Every rerun this budget authorizes is another provider-side workflow run billed to the repository, so the value is read only from the trusted default-branch copy of this file, exactly like `document.instructions` and `disable_project_settings`.
A pushed branch cannot raise its own rerun budget.
The default is `0` because a cancelled conclusion does not identify its cause: the same value covers the provider aborting its own infrastructure, a maintainer stopping a runaway or unsafe job, and repository concurrency with `cancel-in-progress`.
Rerunning on that ambiguity can restart work someone deliberately stopped, so raise this only for a repository whose cancellations are known to be provider-side.

With no trusted copy of this file, the operator's own [`ci.rerun_transient`](/no-slop/reference/global-config/#cirerun_transient) applies, then the built-in default.
A value set here always wins over the global one, so the maintainer of the repository has the last word on how many workflow runs their project is billed for.

A rerun is requested only when the provider itself reported the outcome as `cancelled`, which is the one terminal outcome it attributes to itself rather than to the job:

- `failure`, `error`, `action_required`, and `startup_failure` are the job's own verdict on the commit, so they escalate on the first failure with no added latency.
- `timed_out` means the job exceeded its own `timeout-minutes`, which is usually the branch's own code hanging. Re-running it burns another full timeout window reproducing the same failure, so it is treated as a genuine failure and is not opt-in.
- `stale` is already treated as skipped rather than failed, so it never reaches this decision.
- An outcome no-slop recognizes as none of the above never earns a rerun either.

A single non-cancelled failure, or a merge conflict, suppresses the rerun for that poll: the fix agent is needed regardless, and no rerun can clear a merge conflict.

The budget is per check per run and is spent when the rerun is requested, so a provider that refuses the request cannot be retried in a loop.
Check names are not unique on a pull request, so same-named checks share one budget.

A rerun request returns as soon as the provider accepts it, while the new attempt replaces the cancelled check in the status rollup a moment later.
A poll that still reads the exact completion the rerun was requested for has observed nothing new, so the monitor waits for a bounded couple of polls rather than escalating a check it never actually re-ran.
A provider that accepts a rerun and never publishes it cannot stall the run past that.
Once the provider publishes a conclusive replacement, no-slop durably stops treating that rerun as outstanding while preserving the spent budget; if the exact watched head is then green, the monitor reports `checks-passed` normally.

A cancelled check that no rerun is going to replace pauses the step for user approval when cancellation is the only remaining issue, so the pull request never looks green.
That is a check that came back cancelled after its rerun, and - at the default budget of `0`, once the budget is spent, or on a provider with no rerun API - the cancellation itself: the provider has already published its conclusion for that check and will not publish another one on its own, so there is nothing left for the monitor to wait for.
It does not enter the `auto_fix.ci` loop and never consumes an auto-fix attempt: a cancellation is the provider reporting itself, so there is nothing for the fix agent to repair and no reason to let it edit code the provider never tested.
Answering that gate with `fix` is still honored, and the fix round you asked for is told about the cancelled check alongside any other issue.

Reruns are skipped when:

- The provider has no rerun API (only GitHub implements one today; GitLab, Bitbucket Cloud, and Azure DevOps reach the approval gate without a rerun).
- The check's details link names nothing the provider can re-run, for example a third-party status pointing at an external dashboard, or a link under a workflow run that names no job the API accepts. A link naming one job re-runs that job; a link naming only the workflow run re-runs that run's failed jobs; an unrecognized link is widened into neither.
- The published branch head no longer equals the commit the run delivered. That case terminates with the expected and observed commits instead: re-running checks against a different head would certify a revision this run never produced. See [pipeline steps: CI](/no-slop/reference/pipeline-steps/#ci).

### commit.fix_message

Override the auto-fix commit subject template for this repository.

| | |
| --- | --- |
| Type | `string` |
| Default | Inherits from global config, whose default is `no-slop({{.Step}}): {{.Summary}}` |

The value follows the [global `commit.fix_message` template syntax and validation rules](/no-slop/reference/global-config/#commitfix_message).
That includes the 1,024-byte template limit, 16-placeholder limit, 4,096-byte summary and rendered-subject limits, and rejection of bidi and invisible Unicode format characters.
The setting applies to the Review, Test, Document, and Lint fix path, not commits created by the Rebase, CI, or Push steps.

This non-executing field is read from the pushed branch, so a branch can adopt its own commit convention without enabling `allow_repo_commands`.

### intent

Override transcript-based user-intent extraction settings for this repo.
Fields not set here inherit from global config and then the built-in defaults.

| Field | Type | Default |
| --- | --- | --- |
| `intent.enabled` | `bool` | Inherits from global (default `true`) |
| `intent.threshold` | `float` | Inherits from global (default `0.2`) |
| `intent.slack_days` | `int` | Inherits from global (default `3`) |
| `intent.disabled_readers` | `string[]` | Adds to globally disabled readers |

Valid `disabled_readers` values are `claude`, `codex`, `opencode`, `rovodev`, `pi`, and `copilot`.

### test.evidence

Override where evidence artifacts from the test step are stored.
Fields not set here inherit from global config and then the built-in defaults.

| Field | Type | Default |
| --- | --- | --- |
| `test.evidence.store_in_repo` | `bool` | Inherits from global (default `false`) |
| `test.evidence.dir` | `string` | Inherits from global (default `.no-slop/evidence`) |

By default, test evidence stays in a temporary directory keyed by run ID and is referenced by local path.
Set `store_in_repo: true` to write evidence under `<dir>/<branch-slug>` inside the worktree so push can commit and publish it with the branch.
Branch slashes become nested directories, unsafe branch characters are replaced, and an empty branch slug falls back to the run ID.
If `dir` is absolute, escapes the worktree, points into `.git`, crosses a symlink, or is ignored by Git, no-slop falls back to temporary evidence storage for that run.
