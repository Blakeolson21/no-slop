---
title: Identity Rename Runbook
description: Inventory and rollout stages for the no-mistakes to no-slop rename.
---


This document records stages 1 through 3 of the `no-mistakes` to `no-slop`
identity change. The compatibility release makes the new identity canonical
without moving physical state. The default root remains `~/.no-mistakes` so an
old process and a new process open the same database and socket.

## Inventory and compatibility contract

| Surface | Canonical identity | Compatibility identity | Stage 1 to 3 behavior |
| --- | --- | --- | --- |
| Main command | `no-slop` | `no-mistakes` | Both binaries expose identical commands and help. |
| Short identity | `ns` | `nm` | New plumbing uses `ns`; retained `nm` spellings are compatibility aliases only. |
| Smart-run wrapper | `ns-smart-run` | `nm-smart-run` | The installed wrapper flip is a stage 4 operator action. Both names must target the same wrapper during rollout. |
| Repository config | `.no-slop.yaml` | `.no-mistakes.yaml` | Either file loads. Both files together are accepted only when their parsed settings match. |
| State-root environment | `NS_HOME` | `NM_HOME` | Either selects the same root. Equal duplicate values are accepted and different values are refused. |
| Default physical state | `~/.no-mistakes/` | same path | Deliberately unchanged in this release. This includes `state.sqlite`, `socket`, `daemon.pid`, `daemon.lock`, `config.yaml`, `repos/`, `worktrees/`, `logs/`, `servers/`, and `proctrees/`. |
| Git gate remote | `no-slop` | `no-mistakes` | Init maintains both remotes at the same bare repository. Eject removes both. |
| Gate hooks | `# no-slop ...` and `NS_HOOK_HELPER` | old marker and `NM_HOOK_HELPER` | Managed-hook detection accepts both generations. New hooks invoke the same binary and export both helper markers. |
| Preserved user hook | `pre-receive.no-slop-user` | `pre-receive.no-mistakes-user` | New preservation uses the canonical file and the managed hook falls back to the old companion. |
| Gate migration stamp | `no-slop-gate-config` | prior `no-mistakes-gate-config` | A new daemon refreshes the isolated gate and writes the canonical stamp. Gate contents and live refs remain in the same bare repository. |
| Private Git refs | `refs/no-slop/...` | `refs/no-mistakes/...` | New operations write canonical refs. Recorded legacy ref names remain valid data and are not deleted by this stage. |
| Agent gate marker | `NS_GATE` | `NO_MISTAKES_GATE` | New subprocesses receive both values. Presence of either is the same coarse diagnostic signal; authorization never trusts this marker alone. |
| Bitbucket credentials | `NS_BITBUCKET_EMAIL`, `NS_BITBUCKET_API_TOKEN`, `NS_BITBUCKET_API_BASE_URL` | corresponding `NO_MISTAKES_*` names | Either generation works. Conflicting pairs are refused. |
| Telemetry settings | `NS_TELEMETRY`, `NS_UMAMI_HOST`, `NS_UMAMI_WEBSITE_ID` | corresponding `NO_MISTAKES_*` names | Either generation works, including dev `.env` fallback for host and website ID. Conflicting pairs are refused. |
| Update suppression | `NS_NO_UPDATE_CHECK` | `NO_MISTAKES_NO_UPDATE_CHECK` | Either generation works. Conflicting pairs are refused. |
| IPC timeout | `NS_DAEMON_CONNECT_TIMEOUT` | `NM_DAEMON_CONNECT_TIMEOUT` | Either generation works. Conflicting pairs are refused. |
| Demo mode | `NS_DEMO` | `NM_DEMO` | Either generation works. Conflicting pairs are refused. |
| Unix installer | `NS_INSTALL_DIR`, `NS_LINK_DIR` | `NO_MISTAKES_INSTALL_DIR`, `NO_MISTAKES_LINK_DIR` | The installer rejects conflicting pairs, installs `no-slop`, and creates `no-mistakes` as an alias to the same file. Its default install root remains `~/.no-mistakes/bin`. |
| Windows installer | `NS_INSTALL_DIR` | `NO_MISTAKES_INSTALL_DIR` | The installer rejects conflicting values and installs identical `no-slop.exe` and `no-mistakes.exe` entry points under the old default directory. |
| Managed services | `com.kunchenguid.no-slop.daemon*`, `no-slop-daemon*` | prior `com.kunchenguid.no-mistakes.daemon*`, `no-mistakes-daemon*` | Install and cleanup recognize old service artifacts so an upgrade cannot leave two daemons serving one root. |
| Agent skill | `/no-slop`, `skills/no-slop/SKILL.md` | `/no-mistakes`, `skills/no-mistakes/SKILL.md` | Init installs generated canonical and compatibility skills with the same operating contract. |
| Release assets and workflow | `no-slop-<version>-<os>-<arch>` and `no-slop-required.yml` | old archives and workflow name | New releases and required-workflow references use the canonical identity. Compatibility binary packaging remains explicit. |
| macOS signing identifier | `com.kunchenguid.no-slop` | `com.kunchenguid.no-mistakes` | Developer ID signing uses the renamed executable identifier in this owner-approved phase; the compatibility reset is intentional. |
| Go module and source entry point | `github.com/Blakeolson21/no-slop`, `cmd/no-slop` | former module and `cmd/no-mistakes` | Source imports and the canonical build path use the renamed repository. The legacy source entry point remains a buildable alias. |
| User-facing strings and documentation | `no-slop`, `.no-slop.yaml`, `NS_*` | old examples only where documenting compatibility | CLI output, logs, docs, and examples lead with the canonical identity while state paths continue to show `~/.no-mistakes`. |
| Configuration keys | existing unprefixed YAML keys | same keys | No duplicate renamed key namespace is introduced. File-name aliases resolve before YAML decoding, and divergent duplicate files are refused. |

The E2E-only process controls follow the same rule. Their canonical names are
`NS_TEST_START_DAEMON`, `NS_TEST_DAEMON_START_TIMEOUT`,
`NS_TEST_DAEMON_STOP_TIMEOUT`, `NS_TEST_DAEMON_START_POLL_INTERVAL`,
`NS_DAEMON_HELPER_PROCESS`, `NS_E2E_DAEMON_INVENTORY`,
`NS_E2E_DAEMON_INVENTORY_PARENT`, `NS_E2E_DAEMON_MAX`,
`NS_E2E_REAP_ABANDONED`, and `NS_E2E_REAP_VERBOSE`. The corresponding `NM_*`
names remain compatibility inputs during the transition. Suite wrappers export
both spellings with identical values when they need to interoperate with an
older test binary.

The installed `nm-smart-run` wrapper also has an environment namespace. Its
stage 4 conversion is mechanical: each of these names receives an `NS_`
canonical spelling while the listed `NM_` name remains an equal-value alias:

`NM_ACCOUNT_POLICY_FILE`, `NM_ACCOUNT_REGISTRY_BIN`,
`NM_ACCT_CAPACITY_MAX_WAIT`, `NM_ACCT_CAPACITY_POLL`,
`NM_ACCT_CAPACITY_WAITED`, `NM_ACCT_CONSUMES_CLAUDE`,
`NM_ACCT_CONSUMES_CODEX`, `NM_ACCT_PREMIUM_TOKENS`,
`NM_ACCT_STALE_AFTER_MIN`, `NM_ADMISSION_LIB`, `NM_ADMIT_MAX_CHECKS`,
`NM_ADMIT_SLOT_HELD`, `NM_ADMIT_VALVE_SLEEP`, `NM_ANTI_SLEEP_DISABLE`,
`NM_ANTI_SLEEP_MAX_SECS`, `NM_CODEX_WAKE_DELIVER`,
`NM_CODEX_WAKE_DISABLE`, `NM_CODEX_WAKE_DISPATCH`, `NM_CODEX_WAKE_HELPER`,
`NM_LIVE_ACCOUNTS_CLAUDE`, `NM_LIVE_ACCOUNTS_CODEX`,
`NM_LIVE_HOME_CLAUDE`, `NM_LIVE_HOME_CODEX`, `NM_ROUTE_LOCK_HELD`,
`NM_ROUTE_LOCK_MAX_HOLD_SECS`, `NM_ROUTE_LOCK_PROBE_WAIT_SECS`,
`NM_ROUTE_LOCK_QUEUE_POLL_SECS`, `NM_ROUTE_LOCK_TOKEN`,
`NM_ROUTE_LOCK_WAIT_SECS`, `NM_ROUTE_PENDING_MAX_WAIT_SECS`,
`NM_ROUTE_PENDING_POLL_SECS`, `NM_ROUTE_WATCH_CHECKS`,
`NM_SMART_ACCOUNT_POLICY`, `NM_SMART_ACCOUNT_POLICY_FILE`,
`NM_SMART_ACCOUNT_REGISTRY`, `NM_SMART_ACCOUNT_REGISTRY_FILE`,
`NM_SMART_ACCOUNT_SELECT`, `NM_SMART_AI_STATUS`, `NM_SMART_BUDGET_FEED`,
`NM_SMART_CLAUDE_FAILOVER_MAX`, `NM_SMART_CODEX_RUN`,
`NM_SMART_CODEX_SELECTOR`, `NM_SMART_CONFIG`,
`NM_SMART_DISABLE_CLAUDE_FAILOVER`, `NM_SMART_LEAD`, `NM_SMART_LOG`,
`NM_SMART_POLICY_HELPER`, `NM_SMART_PREFLIGHT`, `NM_SMART_ROUTE_LOCK`,
`NM_SMART_ROUTING_OVERLAYS`, `NM_SMART_ROUTING_POLICY`, and
`NM_SMART_ROUTING_POLICY_LOCAL`.

Related installed helper filenames are `nm-account-policy.mjs`,
`nm-admission.sh`, `nm-babysit*`, `nm-opencode.sh`, `nm-preflight.sh`,
`nm-smart-run-policy.py`, `nm-smart-run.README.md`, and `nm-smart-run.sh`.
They are external to this repository and are changed only in stage 4.

## Isolation proof

The compatibility suite covers three distinct risks:

1. It builds both executable names from the canonical entry point and compares
   their user-visible command behavior.
2. It loads legacy repository configuration through the canonical code path and
   rejects repositories or environments that supply conflicting identities.
3. It starts an isolated daemon using only `NM_HOME`, observes it with the
   canonical binary using only `NS_HOME`, carries a running row in the shared
   SQLite state through completion, and confirms the canonical binary reads the
   completed state. The test root is always a temporary directory.

No rename test may use the default root. The live `~/.no-mistakes/state.sqlite`
and daemon socket are outside this test boundary.

## Rollout runbook for stage 4

Do not perform these steps as part of stages 1 through 3.

1. Confirm the compatibility release is on the `no-slop` default branch and
   deployed binary parity tests pass on every supported platform.
2. Inventory consumer repositories with an exact filename search. The known
   first consumers are `remote-comp`, Master-Orchestrator, and every other
   repository that contains `.no-mistakes.yaml`.
3. For one consumer at a time, confirm it has no destructive daemon lifecycle
   operation in progress. Rename only `.no-mistakes.yaml` to `.no-slop.yaml`,
   commit that change on a feature branch, and send it through the existing
   gate. Never leave both files in the same commit.
4. Update wrapper source and policy helpers so `ns-smart-run` and every `NS_*`
   variable above are canonical. Resolve each old/new pair before doing work,
   reject different simultaneous values, and export equal values under both
   spellings to subprocesses.
5. Install `~/.local/bin/ns-smart-run` as a link to the same wrapper target as
   `~/.local/bin/nm-smart-run`. Keep the old link. Confirm both resolve to the
   same inode or canonical path before changing any callers.
6. Flip scheduled jobs, agent launchers, documentation, and repository
   automation to `ns-smart-run`. Update the external helper filenames only
   after their callers accept both names.
7. Observe at least two full release cycles with no legacy-only invocations,
   no consumer repository using the old config filename, and no live or parked
   run created by a binary that predates the compatibility release.
8. Retire aliases only with a separate owner-approved change. That change must
   include fresh inventory evidence, explicit rollback steps, and tests proving
   a clear error for every retired name.
9. Treat moving `~/.no-mistakes` as its own later migration. Require zero active
   runs, stop the daemon explicitly, take a recoverable backup, move the root
   atomically where possible, update both aliases together, verify the database
   and socket owner, and retain a tested rollback path before restart.

The stage 4 operator must never update the live wrapper, symlinks, state root,
database, socket, or daemon as a side effect of this document.
