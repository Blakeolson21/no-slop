---
title: Installation
description: All install options, prerequisites, update, and uninstall.
---

## macOS / Linux

```sh
curl -fsSL https://raw.githubusercontent.com/Blakeolson21/no-slop/main/docs/install.sh | sh
```

The installer keeps the real binary in `~/.no-mistakes/bin` and exposes `no-slop` through a symlink in `~/.local/bin` or `/usr/local/bin`. It also creates `no-mistakes` as a compatibility link to the same binary. That keeps future rebuilds in a user-owned location instead of rewriting a system binary in place.

It also installs or refreshes the background daemon for you by running `no-slop daemon restart`, preferring a managed service (launchd on macOS, systemd user service on Linux) and falling back to a detached daemon if that path is unavailable. If the restart fails, the install command fails.

Official release binaries installed this way include the default self-hosted telemetry host and website ID. Disable telemetry with `NS_TELEMETRY=0`, or override the host and website ID with `NS_UMAMI_HOST` and `NS_UMAMI_WEBSITE_ID`.

## Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Blakeolson21/no-slop/main/docs/install.ps1 | iex
```

Installs `no-slop.exe`, creates `no-mistakes.exe` as a compatibility entry point to the same build, and restarts the background daemon automatically with `no-slop.exe daemon restart`, preferring a managed Task Scheduler task and falling back to a detached daemon if needed. If the restart fails, the install command fails.

Official release binaries installed this way include the default self-hosted telemetry host and website ID. Disable telemetry with `NS_TELEMETRY=0`, or override the host and website ID with `NS_UMAMI_HOST` and `NS_UMAMI_WEBSITE_ID`.

## Go install

```sh
go install github.com/Blakeolson21/no-slop/cmd/no-slop@latest
```

`go install` builds the CLI without an embedded telemetry website ID, so telemetry stays off by default unless you later set `NS_UMAMI_WEBSITE_ID` at runtime.

## From source

```sh
git clone git@github.com:Blakeolson21/no-slop.git
cd no-slop
make build
make install
```

`make build` embeds the telemetry host from `NS_UMAMI_HOST` in a repo-local `.env` first, then `UMAMI_HOST` from the shell, then the default self-hosted host. It embeds the telemetry website ID from `NS_UMAMI_WEBSITE_ID` in `.env` first, then `UMAMI_WEBSITE_ID` from the shell, then the default website ID.

## Prerequisites

- **git** - required
- **One supported agent runner** - `claude`, `codex`, `acli` (Rovo Dev), `opencode`, `pi`, or `copilot`, or a configured Cursor/ACP runner such as `agent: cursor`; see [Global Config](/no-slop/reference/global-config/) for ACP requirements
- **Optional, for PRs and CI:**
  - `gh` CLI (GitHub)
  - `glab` CLI (GitLab)
  - `NS_BITBUCKET_EMAIL` and `NS_BITBUCKET_API_TOKEN` (Bitbucket Cloud)
  - `az` CLI with the `azure-devops` extension (Azure DevOps)

Run `no-slop doctor` to check native agents, ACP aliases such as `cursor`, provider tools, and whether the configured global runner can start a validation gate.
Every validation gate requires a runnable pipeline agent and otherwise fails before its first pipeline step.

See [Provider Integration](/no-slop/guides/provider-integration/) for PR and CI setup per host.

## Update

`no-slop update` is disabled in this build, and so are the background update checks behind it. Update explicitly with the installer or rebuild from a trusted `Blakeolson21/no-slop` checkout.

Rebuild from a checkout instead, then restart the daemon so it picks up the new executable:

```sh
go build -o ~/.no-mistakes/bin/no-slop.new ./cmd/no-slop
mv ~/.no-mistakes/bin/no-slop.new ~/.no-mistakes/bin/no-slop
no-slop daemon restart
```

Target the real binary from the layout above, not the symlink: `go build -o` leaves a symlink in place and truncates its target, which fails with `ETXTBSY` on Linux while the daemon is executing that file. Stage the build in the same directory so the `mv` is an atomic rename rather than a cross-filesystem copy from `/tmp`.

The install directory comes from `NS_INSTALL_DIR` and defaults to `~/.no-mistakes/bin`; `NS_HOME` does not affect it. A `go install` layout puts the binary in `GOBIN` instead.

The [CLI reference](/no-slop/reference/cli/#no-slop-update) owns what the disabled command does with each flag; [Daemon & Worktrees](/no-slop/concepts/daemon/#starting-and-stopping) owns the active-run guard on the restart.

## Remove from a repo

```sh
no-slop eject
```

Removes the `no-slop` remote, deletes the bare repo, cleans up worktrees, and removes the database record.
It does not remove repo-local agent skill files created by `no-slop init`.

## Uninstall

Stop the daemon, delete the binary, and clear state:

```sh
no-slop daemon stop
rm -f ~/.local/bin/no-slop /usr/local/bin/no-slop
rm -rf ~/.no-mistakes
```

On macOS, also remove `~/Library/LaunchAgents/com.kunchenguid.no-slop.daemon.*.plist`. On Linux, also remove `~/.config/systemd/user/no-slop-daemon-*.service`. On Windows, remove the `no-slop-daemon-*` Task Scheduler task.
