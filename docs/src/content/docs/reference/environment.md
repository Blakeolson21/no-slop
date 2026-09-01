---
title: Environment Variables
description: All environment variables recognized by no-slop.
---

## `NS_HOME`

Override the data directory.

|         |                  |
| ------- | ---------------- |
| Type    | `string`         |
| Default | `~/.no-mistakes` |

When set, everything else moves under this root:

- Global config: `$NS_HOME/config.yaml`
- Gate repos: `$NS_HOME/repos/<id>.git`
- Worktrees: `$NS_HOME/worktrees/<repoID>/<runID>/`
- Logs: `$NS_HOME/logs/`
- Database: `$NS_HOME/state.sqlite`
- Socket / PID / singleton lock: `$NS_HOME/socket`, `$NS_HOME/daemon.pid`, and `$NS_HOME/daemon.lock`
- Managed agent server PID records: `$NS_HOME/servers/`
- Process-tree recovery records: `$NS_HOME/proctrees/`
- Managed service names get a short stable suffix derived from `$NS_HOME` so multiple installs don't collide.

`NM_HOME` is a compatibility alias. If both names are set, their values must
resolve to the same root, including when one value is explicitly empty, or the
command refuses to choose a state root.

## `NS_DAEMON_CONNECT_TIMEOUT`

Override how long a CLI client waits for an existing daemon socket to accept a connection before failing instead of hanging.

|         |                                                                                                   |
| ------- | ------------------------------------------------------------------------------------------------- |
| Type    | `string` (Go duration)                                                                            |
| Default | unset (falls back to the `daemon_connect_timeout` global config value, itself defaulting to `3s`) |

Takes precedence over `daemon_connect_timeout` in `config.yaml`. An empty, unparsable, or non-positive value is ignored and the config value (or its default) is used instead.

`NM_DAEMON_CONNECT_TIMEOUT` is a compatibility alias. Conflicting values,
including empty versus non-empty, are returned as alias-conflict errors.

## `NS_QUARTERMASTER_BIN`

Lease authority executable used when [`quartermaster.enabled`](/no-slop/reference/global-config/#quartermaster) is true and `quartermaster.bin` is left blank.

|         |                                     |
| ------- | ----------------------------------- |
| Type    | `string`                            |
| Default | `~/.fleet/scripts/quartermaster.py` |

A configured `quartermaster.bin` takes precedence over this variable.

## `NS_QUARTERMASTER_ACCOUNT_REGISTRY`

Path to the JSON registry that maps a leased account slug to the account home no-slop binds for the invocation.

|         |                                                     |
| ------- | --------------------------------------------------- |
| Type    | `string`                                            |
| Default | `~/.no-mistakes-dashboard/execution-accounts.json` |

Each entry names a `slug`, a `provider` (`claude`/`anthropic` or `codex`/`openai`), and a `home`.
When the registry is missing, unreadable, or has no matching entry, no-slop falls back to `~/.claude-accounts/<account>` or `~/.codex-accounts/<account>`; if that directory does not exist, the lease is refused and the step fails.

no-slop also *sets* `NS_QUARTERMASTER_ACCOUNT`, `NS_QUARTERMASTER_LEASE_ID`, and `NS_QUARTERMASTER_POOL` (plus `CLAUDE_CONFIG_DIR` or `CODEX_HOME`) in the leased agent process. They are outputs of a lease, not inputs you set.

## `NS_BITBUCKET_EMAIL`

Bitbucket Cloud account email used for PR creation and CI monitoring.

|         |                                               |
| ------- | --------------------------------------------- |
| Type    | `string`                                      |
| Default | (none; Bitbucket PR/CI steps skip when unset) |

Used alongside `NS_BITBUCKET_API_TOKEN`. `NO_MISTAKES_BITBUCKET_EMAIL` is a
compatibility alias; if both names are set, their values must match, including
empty values. See [Provider Integration](/no-slop/guides/provider-integration/#bitbucket-cloud).

## `NS_BITBUCKET_API_TOKEN`

Bitbucket Cloud API token.

|         |          |
| ------- | -------- |
| Type    | `string` |
| Default | (none)   |

Get one from [Bitbucket account settings](https://bitbucket.org/account/settings/app-passwords/).

`NO_MISTAKES_BITBUCKET_API_TOKEN` is a compatibility alias; if both names are
set, their values must match, including empty values.

## `NS_BITBUCKET_API_BASE_URL`

Override the Bitbucket Cloud API base URL.

|         |                                 |
| ------- | ------------------------------- |
| Type    | `string`                        |
| Default | `https://api.bitbucket.org/2.0` |

Useful for mocking in tests or pointing at a proxy.

`NO_MISTAKES_BITBUCKET_API_BASE_URL` is a compatibility alias; if both names are
set, their values must match, including empty values.

## `AZURE_DEVOPS_EXT_PAT`

Azure DevOps Personal Access Token inherited by the daemon for non-interactive `az` CLI auth.
Alternatively, authenticate the Azure DevOps extension with `az devops login`.

|         |                                                    |
| ------- | -------------------------------------------------- |
| Type    | `string`                                           |
| Default | (none)                                             |

See [Provider Integration](/no-slop/guides/provider-integration/#azure-devops).

## `GITHUB_TOKEN`

GitHub token used to authenticate updater release requests.

|         |          |
| ------- | -------- |
| Type    | `string` |
| Default | (none)   |

When set, the updater sends the token as a Bearer authorization header for release metadata requests, including background update checks, and release asset downloads. `GITHUB_TOKEN` takes precedence over `GH_TOKEN`; when neither variable is set, these requests remain anonymous. The token is not printed, logged, or persisted.

This variable no longer affects update behavior in this build: self-update and its background checks are disabled, so the updater never makes a release request. See the [CLI reference](/no-slop/reference/cli/#no-slop-update).
It still matters everywhere else. The `gh` CLI that the PR and CI steps shell out to inherits the daemon's environment and authenticates from it, so unsetting the token breaks PR creation and CI polling.

## `GH_TOKEN`

Fallback GitHub token when `GITHUB_TOKEN` is unset or empty. The `gh` CLI behind the PR and CI steps reads it as well.

|         |          |
| ------- | -------- |
| Type    | `string` |
| Default | (none)   |

See [`GITHUB_TOKEN`](#github_token) for the updater's authentication behavior and precedence.

## `NS_NO_UPDATE_CHECK`

Disable background update checks.

|         |                                                |
| ------- | ---------------------------------------------- |
| Type    | `1` to disable, anything else to leave enabled |
| Default | unset (checks enabled)                         |

Update checks run on every CLI invocation except `update` itself and version queries (`--version` / `-v`, which stay side-effect-free), hit GitHub releases, cache the result in `$NS_HOME/update-check.json`, and print a one-line notification to stderr when a newer version is available. Dev builds (non-semver versions) suppress the check automatically.

This variable has no effect in this build: self-update is disabled, so background checks never run whether or not it is set. See the [CLI reference](/no-slop/reference/cli/#no-slop-update).

`NO_MISTAKES_NO_UPDATE_CHECK` is a compatibility alias; if both names are set,
their values must match, including empty values.

## `NS_INSTALL_DIR`

Override the install directory used by the repository install scripts.

|         |                                                                 |
| ------- | --------------------------------------------------------------- |
| Type    | `string`                                                        |
| Default | Unix: `~/.no-mistakes/bin`; Windows: `%LOCALAPPDATA%\no-mistakes` |

This controls where `docs/install.sh` places the real binary and where
`docs/install.ps1` places `no-slop.exe` and its `no-mistakes.exe` compatibility
entry point. It does not relocate no-slop state; use [`NS_HOME`](#ns_home) for
that.

`NO_MISTAKES_INSTALL_DIR` is a compatibility alias; if both names are set, their
values must match, including empty values.

## `NS_LINK_DIR`

Override the symlink directory used by the Unix install script.

|         |                                                                                            |
| ------- | ------------------------------------------------------------------------------------------ |
| Type    | `string`                                                                                   |
| Default | `~/.local/bin` when it is already on `PATH`; otherwise `/usr/local/bin` |

`docs/install.sh` links both `no-slop` and the `no-mistakes` compatibility
command into this directory. The PowerShell installer does not use this
variable because it adds the install directory itself to the user `Path`.

`NO_MISTAKES_LINK_DIR` is a compatibility alias; if both names are set, their
values must match, including empty values.

## `XDG_DATA_HOME`

Data directory used to discover OpenCode transcripts for intent extraction.

|         |                  |
| ------- | ---------------- |
| Type    | `string`         |
| Default | `~/.local/share` |

When set, no-slop looks for OpenCode's intent transcript database at `$XDG_DATA_HOME/opencode/opencode.db`.
When unset, it falls back to `~/.local/share/opencode/opencode.db`.

## `GLAB_CONFIG_DIR`

Directory holding glab's `config.yml`, consulted when detecting self-hosted GitLab.

|         |          |
| ------- | -------- |
| Type    | `string` |
| Default | (none)   |

When the upstream hostname carries no `gitlab` marker, no-slop reads glab's configured hosts from `$GLAB_CONFIG_DIR/config.yml` to decide whether the host is a GitLab instance. It takes precedence over `XDG_CONFIG_HOME`. See [Provider Integration](/no-slop/guides/provider-integration/#self-hosted-githubgitlab).

## `GH_CONFIG_DIR`

Directory holding gh's `hosts.yml`, consulted when detecting self-hosted GitHub Enterprise.

|         |          |
| ------- | -------- |
| Type    | `string` |
| Default | (none)   |

When the upstream hostname is not `github.com`, no-slop reads gh's configured hosts from `$GH_CONFIG_DIR/hosts.yml` to decide whether the host is a GitHub Enterprise instance. It takes precedence over `XDG_CONFIG_HOME`. See [Provider Integration](/no-slop/guides/provider-integration/#self-hosted-githubgitlab).

## `XDG_CONFIG_HOME`

Config directory used to locate glab's `config.yml` for self-hosted GitLab detection and gh's `hosts.yml` for self-hosted GitHub Enterprise detection.

|         |             |
| ------- | ----------- |
| Type    | `string`    |
| Default | `~/.config` |

When `GLAB_CONFIG_DIR` is unset, no-slop looks for glab's configured hosts at `$XDG_CONFIG_HOME/glab-cli/config.yml`, falling back to `~/.config/glab-cli/config.yml` when `XDG_CONFIG_HOME` is unset.
When `GH_CONFIG_DIR` is unset, no-slop looks for gh's configured hosts at `$XDG_CONFIG_HOME/gh/hosts.yml`, falling back to `~/.config/gh/hosts.yml` when `XDG_CONFIG_HOME` is unset.

## `NS_UMAMI_HOST`

Override the telemetry collection host.

|         |                             |
| ------- | --------------------------- |
| Type    | `URL`                       |
| Default | `https://a.kunchenguid.com` |

When set, telemetry sends events to this host's `/api/send` endpoint. `NO_MISTAKES_UMAMI_HOST` is the compatibility alias; if both names are set, their values must match, including empty values. If it is unset in a dev build, `no-slop` also checks a repo-local `.env` file for either spelling. If no runtime value is found, it falls back to any host embedded at build time and then the default self-hosted Umami instance.

## `NS_UMAMI_WEBSITE_ID`

Override or enable the telemetry website ID.

|         |                                                                         |
| ------- | ----------------------------------------------------------------------- |
| Type    | `string`                                                                |
| Default | embedded in Makefile and release builds; unset in unembedded dev builds |

When set, telemetry uses this website ID at runtime. `NO_MISTAKES_UMAMI_WEBSITE_ID` is the compatibility alias; if both names are set, their values must match, including empty values. If it is unset in a dev build, `no-slop` also checks a repo-local `.env` file for either spelling. If no runtime value is found, it falls back to any website ID embedded at build time.

When telemetry is enabled, `no-slop` sends command, run, approval, fix, and wizard events, completed step events with `awaiting_approval`, `fix_review`, or `failed` status, and pageviews for the human surfaces `/wizard` and `/tui` and the state-changing agent surfaces `/axi/run`, `/axi/respond`, and `/axi/abort` to Umami.
Mutation pageviews are sent alongside command events, so command status and duration remain available.
They include only flag-derived context: `/axi/run` records whether `--yes`, `--intent`, or `--skip` was present, and `/axi/respond` records the sanitized action and whether `--yes` was present.

Read-only surfaces (`axi` home, `axi status`, `axi logs`, `status`, `runs`) emit no pageview and rate-limit their command event: it is sent when the observed run state changed since the last emit, and otherwise at most once per 10 minutes, with the dedupe state persisted at `<NS_HOME>/telemetry-gate.json` so agent polling loops stay bounded across processes.
The `axi logs` command event records the sanitized step, whether `--full` was present, and whether `--run` was present; `axi status` records whether `--run` was present.
Each explicit human CLI, AXI, or TUI branch-sync check/apply attempt emits one command event and no additional pageview.
Its fields are bounded enums and booleans only: surface, mode, state, relation, target kind, pipeline phase, PR state, result, refusal reason, dirty state, and duration.
It never sends a SHA, run ID, path, branch name, URL, remote name, or command argument.

### What stays local and what leaves the machine

Everything sent remotely is low-cardinality: command names, statuses, durations, counts, flag booleans, agent and step names, and - on the single terminal `run finished` event - the bounded performance rollup `agent_invocations`, `resumed_invocations`, and `fallback_invocations` (small counts only).
Run IDs, repository paths, branch names, session identities, prompts, model outputs, diffs, and per-invocation performance records are never sent.

Detailed performance evidence stays on the machine in the local state database (`<NS_HOME>/state.sqlite`): one `agent_invocations` row per agent invocation, plus each run's accumulated parked-at-gate time.
Each row records run and step identity, purpose (such as review/review-fix/housekeeping), the reported model and its provider, the cold/started/resumed/fallback session mode, a truncated session-identity hash, timestamps, duration, exit status, and failure category (`quota` when provider quota exhaustion is what failed the invocation, whether the lane was skipped while recorded as exhausted or hit the banner live), alongside the session-fidelity metrics below.
It never stores prompts, model outputs, diffs, raw command arguments, secret values, or credentials - only bounded counts, low-cardinality categories, and durations.

The additive session-fidelity fields are nullable and read back as unknown (rendered `-`) rather than a fabricated zero when the adapter did not report them, so rows written before a field existed, and adapters that do not surface a datum, stay honest.
The legacy raw input, output, and cache-read token counters render numerically; use the nullable per-round and derived fields to determine whether the adapter reported comparable usage:

- Token detail: `input_tokens`/`output_tokens`/`cache_read_tokens` (raw, cumulative across a resumed session for codex), `fresh_input_tokens` (input minus cache reads), `cache_creation_tokens` (unknown when the provider does not surface it), `reasoning_tokens`, and `delta_input_tokens`/`delta_output_tokens`/`delta_cache_read_tokens` (the correct per-round amounts, so a resumed session's cumulative counter is never mistaken for one round's usage).
- Activity: `model_roundtrips` (a proxy for productive model turns), `tool_calls`, and a bounded tool-category histogram (`tool_wait_calls`, `tool_test_lint_calls`, `tool_edit_calls`, `tool_read_calls`, `tool_git_calls`, `tool_other_calls`); a compound command counts once per sub-command, so the histogram can sum higher than `tool_calls`.
- Timing split: `subprocess_wait_ms` is the wall-clock spent inside tool subprocesses; model/reasoning time is the invocation duration minus it, clamped at zero.
- Context: `workload_files`/`workload_lines` (bounded change size), `finding_count` (findings in the structured output), and `fallback_reason` (why a failed resume forced a fresh session, one of transient/parse/exit/spawn/unsupported/quota/other; `quota` means the session's lane was quota-exhausted).

The count and timing definitions live in one authoritative place (`internal/agent/invocationmetrics.go`).
Inspect the evidence with `no-slop stats --agents` (per-purpose aggregates, including a `METRICS` coverage count so a real zero is distinguishable from missing instrumentation) or `no-slop stats --run <id>` (one run's invocations, the per-round-vs-cumulative token split, and parked time).

## `NS_TELEMETRY`

Disable telemetry collection.

|         |                                                                   |
| ------- | ----------------------------------------------------------------- |
| Type    | `0`, `false`, or `off` to disable; anything else to leave enabled |
| Default | unset                                                             |

When set to a disabling value, telemetry stays off even if a runtime or embedded website ID is available. `NO_MISTAKES_TELEMETRY` is the compatibility alias; if both names are set, their values must match, including empty values.

## Environment the daemon sees

When the daemon runs through a managed service (launchd, systemd user service, Task Scheduler), the macOS and Linux service definitions include a default `PATH` with common user and system binary directories. They also bake in any proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`) that were set when you installed or refreshed the service, so the daemon and the agents it spawns can reach the network through your proxy even when the login-shell probe is unavailable. Once baked in, the values are preserved across later service refreshes and restarts even when the proxy variables are not exported in that shell, so a routine `daemon restart` or a binary upgrade will not strip them; export the variables again only when you need to change or remove them. Both the upper- and lower-case spellings are forwarded exactly as you set them, because tooling is inconsistent about which it reads (curl, for example, honors only the lower-case `http_proxy` for plain-HTTP requests). Because a proxy URL can embed credentials (for example `http://user:pass@host`), the generated service file is restricted to owner-only `0600` permissions whenever proxy values are forwarded into it. When no proxy variables are set, the generated definition is unchanged and keeps the conventional `0644` mode. Windows Task Scheduler inherits your logon environment and needs no forwarding. At daemon startup, the daemon resolves environment from your login shell on macOS and Linux, preserves your shell `PATH` order, and appends any missing well-known directories such as `~/.local/bin`, `~/go/bin`, `~/.cargo/bin`, `~/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, `/usr/bin`, and `/bin`. If login-shell resolution fails or returns no entries, the daemon logs a warning and uses an augmented process-environment fallback that may omit version-manager directories such as nvm, fnm, or volta. On Windows it reuses the current process environment.

If your env vars aren't set in your login shell's rc files (`.zprofile`, `.zshrc`, `.profile`, `.bash_profile`, `.bashrc`, PowerShell profile), the daemon won't see them. Put them somewhere a login shell will load, then restart the daemon to pick them up.
