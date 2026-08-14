#!/usr/bin/env bash
# Suite wrapper for temporary E2E daemon ownership.
#
# Responsibilities:
#   1. Exact inventory directory (mode 0700) for this suite invocation
#   2. EXIT/INT/TERM trap that reaps only inventoried temp daemons
#   3. Concurrency cap via NS_E2E_DAEMON_MAX (default 2)
#   4. Pre-reap of any leftover inventory from a prior killed wrapper
#
# Honest boundary: this EXIT trap does NOT survive SIGKILL of this shell.
# When the wrapper itself is SIGKILL'd, the on-disk inventory is recovered
# on the next suite start (this script's pre-reap + package TestMain).
# Child go-test interruption/timeout/SIGKILL is covered: this shell still
# runs the trap and reaps.
set -u

resolve_alias() {
  local canonical_name="$1"
  local legacy_name="$2"
  local canonical_value=""
  local legacy_value=""
  local canonical_set=0
  local legacy_set=0
  if declare -p "$canonical_name" >/dev/null 2>&1; then
    canonical_set=1
    canonical_value="${!canonical_name}"
  fi
  if declare -p "$legacy_name" >/dev/null 2>&1; then
    legacy_set=1
    legacy_value="${!legacy_name}"
  fi
  if [[ "$canonical_set" -eq 1 && "$legacy_set" -eq 1 && "$canonical_value" != "$legacy_value" ]]; then
    printf '%s and legacy alias %s configure the same setting with different values\n' "$canonical_name" "$legacy_name" >&2
    return 2
  fi
  if [[ "$canonical_set" -eq 1 ]]; then
    printf '%s' "$canonical_value"
  else
    printf '%s' "$legacy_value"
  fi
}

assign_alias() {
  local target="$1"
  local value
  shift
  if value="$(resolve_alias "$@")"; then
    printf -v "$target" '%s' "$value"
  else
    exit "$?"
  fi
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

assign_alias NS_E2E_DAEMON_INVENTORY NS_E2E_DAEMON_INVENTORY NM_E2E_DAEMON_INVENTORY
assign_alias NS_E2E_DAEMON_INVENTORY_PARENT NS_E2E_DAEMON_INVENTORY_PARENT NM_E2E_DAEMON_INVENTORY_PARENT

if [[ -z "$NS_E2E_DAEMON_INVENTORY" ]]; then
  base="/tmp"
  if [[ -d /private/tmp ]]; then
    base="/private/tmp"
  fi
  NS_E2E_DAEMON_INVENTORY_PARENT="${base}/no-mistakes-e2e-inventories-$(id -u)"
  if [[ -L "$NS_E2E_DAEMON_INVENTORY_PARENT" ]]; then
    exit 1
  fi
  mkdir -p "$NS_E2E_DAEMON_INVENTORY_PARENT" || exit 1
  chmod 700 "$NS_E2E_DAEMON_INVENTORY_PARENT" || exit 1
  NS_E2E_DAEMON_INVENTORY="$(mktemp -d "${NS_E2E_DAEMON_INVENTORY_PARENT}/run-XXXXXX")" || exit 1
  chmod 700 "$NS_E2E_DAEMON_INVENTORY" || exit 1
  printf '%s\n' "$$" >"$NS_E2E_DAEMON_INVENTORY/owner.pid" || exit 1
  chmod 600 "$NS_E2E_DAEMON_INVENTORY/owner.pid" || exit 1
  OWNED_INVENTORY=1
else
  mkdir -p "$NS_E2E_DAEMON_INVENTORY"
  chmod 700 "$NS_E2E_DAEMON_INVENTORY" 2>/dev/null || true
  OWNED_INVENTORY=0
fi

assign_alias NS_E2E_DAEMON_MAX NS_E2E_DAEMON_MAX NM_E2E_DAEMON_MAX
NS_E2E_DAEMON_MAX="${NS_E2E_DAEMON_MAX:-2}"

# Export both spellings with the same values so old and new test binaries can
# participate in one isolated suite during the compatibility window.
export NS_E2E_DAEMON_INVENTORY NS_E2E_DAEMON_INVENTORY_PARENT NS_E2E_DAEMON_MAX
export NM_E2E_DAEMON_INVENTORY="$NS_E2E_DAEMON_INVENTORY"
export NM_E2E_DAEMON_INVENTORY_PARENT="$NS_E2E_DAEMON_INVENTORY_PARENT"
export NM_E2E_DAEMON_MAX="$NS_E2E_DAEMON_MAX"

reap_inventory() {
  # Best-effort; never expand into shared-daemon territory (reaper refuses).
  (cd "$ROOT" && go run ./internal/e2edaemon/reapmain.go) >/dev/null 2>&1 || true
}

if [[ -n "${NS_E2E_DAEMON_INVENTORY_PARENT:-}" ]]; then
  export NS_E2E_REAP_ABANDONED=1
  export NM_E2E_REAP_ABANDONED=1
  reap_inventory
  unset NS_E2E_REAP_ABANDONED
  unset NM_E2E_REAP_ABANDONED
fi

trap 'reap_inventory; if [[ "${OWNED_INVENTORY}" -eq 1 ]]; then rm -rf "$NS_E2E_DAEMON_INVENTORY" 2>/dev/null || true; fi' EXIT INT TERM

# Default args match the historical Makefile e2e target; callers may override.
if [[ "$#" -eq 0 ]]; then
  # Individual operations keep their own short timeouts. The package-wide
  # ceiling only needs to cover the complete serial journey inventory, which
  # is currently longer than five minutes on supported development hosts.
  set -- -tags=e2e -count=1 -timeout 600s ./internal/e2e/... ./internal/pipeline/steps/...
fi

go test "$@"
code=$?
exit "$code"
