#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 conditioned|unconditioned OUTPUT.json" >&2
  exit 2
fi

policy=$1
output=$2
if [[ "$policy" != "conditioned" && "$policy" != "unconditioned" ]]; then
  echo "policy must be conditioned or unconditioned" >&2
  exit 2
fi

for command_name in git go jq opencode python3 timeout; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command not found: $command_name" >&2
    exit 2
  fi
done

repo_root=$(git rev-parse --show-toplevel)
campaign="$repo_root/corpus/campaign.json"
corpus_root="$repo_root/corpus/seeds"
policy_file="$repo_root/corpus/policies/$policy.md"
challenge_file="$repo_root/corpus/policies/challenge.md"
model=${NOSLOP_EVAL_MODEL:-opencode/deepseek-v4-flash-free}
timeout_seconds=${NOSLOP_EVAL_TIMEOUT_SECONDS:-30}
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/noslop-corpus.XXXXXX")
trap 'rm -rf "$temp_dir"' EXIT

now_ms() {
  python3 -c 'import time; print(time.time_ns() // 1000000)'
}

mkdir -p "$(dirname "$output")"
results_tmp="$temp_dir/results.json"
run_tmp="$temp_dir/run.json"
jq --arg policy "$policy" '{schema_version: 1, policy: $policy, cases: [.cases[] | {case_id, findings: []}]}' "$campaign" >"$results_tmp"
jq -n \
  --arg schema_version "1" \
  --arg policy "$policy" \
  --arg model "$model" \
  --argjson timeout_seconds "$timeout_seconds" \
  --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schema_version: ($schema_version | tonumber), policy: $policy, model: $model, timeout_seconds: $timeout_seconds, created_at: $created_at, invocations: []}' >"$run_tmp"

mechanical="$temp_dir/mechanical.json"
(cd "$repo_root" && go run ./cmd/noslop-corpus-replay --corpus "$corpus_root" --campaign "$campaign") >"$mechanical"
jq --slurpfile mechanical "$mechanical" '
  reduce $mechanical[0].cases[] as $case (.;
    .cases |= map(if .case_id == $case.case_id then .findings = $case.findings else . end)
  )
' "$results_tmp" >"$temp_dir/results-next.json"
mv "$temp_dir/results-next.json" "$results_tmp"
jq --slurpfile mechanical "$mechanical" '
  .invocations += ($mechanical[0].cases | map({kind: "mandatory", case_aliases: [.alias], tier: .tier, round: 0, elapsed_ms: .elapsed_ms, findings: .findings}))
' "$run_tmp" >"$temp_dir/run-next.json"
mv "$temp_dir/run-next.json" "$run_tmp"

lenses=(
  vacuous-check
  test-capitulation
  self-consistent-oracle
  comment-defended-workaround
  scope-expansion
  asserted-followup-without-artifact
  fail-open-default
  rule-applied-in-one-place-not-sibling
  redundant-comment
)

for lens in "${lenses[@]}"; do
  for tier in single-review full-adversarial; do
    packet="$temp_dir/packet.json"
    packet_base="$temp_dir/packet-base.json"
    jq --arg lens "$lens" --arg tier "$tier" '
      [.cases[] | select(.tier == $tier and (.conditioning_lenses | index($lens))) | {case_id, alias, tier, intent}]
    ' "$campaign" >"$packet_base"
    jq -n --slurpfile selected "$packet_base" '
      $selected[0] | map(. + {diff: ""})
    ' >"$packet"
    packet_count=$(jq 'length' "$packet")
    if [[ "$packet_count" -eq 0 ]]; then
      continue
    fi

    while IFS= read -r alias; do
      number=${alias#case-}
      number=$((10#$number))
      printf -v case_prefix '%02d' "$number"
      matches=("$corpus_root/$case_prefix-"*)
      if [[ ${#matches[@]} -ne 1 || ! -d "${matches[0]}" ]]; then
        echo "could not resolve $alias to one case directory" >&2
        exit 1
      fi
      jq --arg alias "$alias" --rawfile diff "${matches[0]}/change.diff" '
        map(if .alias == $alias then .diff = $diff else . end)
      ' "$packet" >"$temp_dir/packet-next.json"
      mv "$temp_dir/packet-next.json" "$packet"
    done < <(jq -r '.[].alias' "$packet")

    rounds=1
    if [[ "$tier" == "full-adversarial" || "$policy" == "conditioned" ]]; then
      rounds=2
    fi
    prior='{"findings":[]}'
    for ((round = 1; round <= rounds; round++)); do
      prompt=$(cat "$policy_file")
      if [[ "$policy" == "conditioned" ]]; then
        prompt+=$'\n\nPriority lens from provenance: `'
        prompt+="$lens"
        prompt+=$'`.'
      fi
      if [[ "$round" -eq 2 ]]; then
        prompt+=$'\n\n'
        prompt+=$(cat "$challenge_file")
      fi
      prompt+=$'\n\nCase packet:\n'
      prompt+=$(jq -c '.' "$packet")
      prompt+=$'\n\nPrior-round output:\n'
      prompt+="$prior"

      started=$(now_ms)
      set +e
      raw=$(cd "$temp_dir" && timeout "$timeout_seconds" opencode run --model "$model" "$prompt")
      invocation_status=$?
      set -e
      finished=$(now_ms)
      elapsed=$((finished - started))
      invocation_error=""
      if [[ "$invocation_status" -eq 124 ]]; then
        invocation_error="timed out after ${timeout_seconds}s"
        structured='{"findings":[]}'
      elif [[ "$invocation_status" -ne 0 ]]; then
        invocation_error="opencode exited with status $invocation_status"
        structured='{"findings":[]}'
      else
        structured=$(printf '%s\n' "$raw" | sed -e '/^[[:space:]]*```json[[:space:]]*$/d' -e '/^[[:space:]]*```[[:space:]]*$/d')
      fi
      if ! jq -e '.findings | type == "array"' >/dev/null 2>&1 <<<"$structured"; then
        echo "invalid structured output for $policy $lens $tier round $round" >&2
        echo "$raw" >&2
        exit 1
      fi
      if ! jq -e --slurpfile packet "$packet" '
        all(.findings[]; (.case_id as $id | any($packet[0][]; .alias == $id))) and
        all(.findings[]; .lens == "vacuous-check" or .lens == "test-capitulation" or .lens == "self-consistent-oracle" or .lens == "comment-defended-workaround" or .lens == "scope-expansion" or .lens == "asserted-followup-without-artifact" or .lens == "fail-open-default" or .lens == "rule-applied-in-one-place-not-sibling" or .lens == "redundant-comment")
      ' >/dev/null <<<"$structured"; then
        echo "output used an unknown case alias or finding lens" >&2
        echo "$raw" >&2
        exit 1
      fi

      normalized="$temp_dir/normalized.json"
      jq --slurpfile campaign "$campaign" '
        {findings: [.findings[] | .case_id as $alias | .case_id = ($campaign[0].cases[] | select(.alias == $alias) | .case_id)]}
      ' <<<"$structured" >"$normalized"
      jq --slurpfile observed "$normalized" '
        reduce $observed[0].findings[] as $finding (.;
          .cases |= map(if .case_id == $finding.case_id then .findings += [($finding | del(.case_id))] else . end)
        ) |
        .cases |= map(.findings |= unique_by(.lens, .path, .line))
      ' "$results_tmp" >"$temp_dir/results-next.json"
      mv "$temp_dir/results-next.json" "$results_tmp"

      aliases=$(jq '[.[].alias]' "$packet")
      jq \
        --arg kind "review" \
        --arg lens "$lens" \
        --arg tier "$tier" \
        --argjson round "$round" \
        --argjson elapsed "$elapsed" \
        --argjson aliases "$aliases" \
        --argjson response "$structured" \
        --arg raw_response "$raw" \
        --arg error "$invocation_error" \
        '.invocations += [{kind: $kind, priority_lens: $lens, case_aliases: $aliases, tier: $tier, round: $round, elapsed_ms: $elapsed, response: $response, raw_response: $raw_response, error: (if $error == "" then null else $error end)}]' \
        "$run_tmp" >"$temp_dir/run-next.json"
      mv "$temp_dir/run-next.json" "$run_tmp"
      prior="$structured"
    done
  done
done

jq '.cases |= sort_by(.case_id)' "$results_tmp" >"$output"
run_output="${output%.json}.run.json"
jq '.total_elapsed_ms = ([.invocations[].elapsed_ms] | add // 0)' "$run_tmp" >"$run_output"
echo "wrote $output" >&2
echo "wrote $run_output" >&2
