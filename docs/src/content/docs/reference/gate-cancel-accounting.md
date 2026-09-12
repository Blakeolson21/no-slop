---
title: Gate cancellation and terminal usage
---

`axi abort --run <id> --reason "..."` explicitly cancels one run. A missing or
empty ID is refused. `reason` is preserved on the run before cancellation.
The CLI never treats a missing/wrong ID as permission to cancel a neighboring run.

A lane is `(repository store ID, branch)`. `gate_aborts` retains each cancelled
run ID once, including cancellation rows predating this migration. Updating a
status later, restarting the daemon, deleting a run, or retrying an abort cannot
refund the budget. The third and every later dispatch after two cancellations
requires an independent signed, single-use adjudication. The run INSERT checks
and consumes that grant atomically, before creating its worktree or invoking an
agent. A refused dispatch spends no provider turn.

The operator provisions a base64 Ed25519 public key at
`<NS_HOME>/cancel-budget-public-key`. Its private key stays with the independent
coordinator. There is no key creation, reset, waive, or self-sign command.
The submitting lane must never provision or replace this operator key.

The signed envelope is JSON with base64 `payload` and `signature`. The signature
covers the exact payload bytes. The payload is JSON containing `repo_id`,
`branch`, `head_sha`, sorted `aborted_run_ids`, `reason`, and `issuer`.
All historical abort IDs for that lane must match, and the candidate HEAD is
bound to the grant. The issuer must be nonempty. Register it with:

```sh
no-slop axi cancel-adjudication --record /path/to/signed.json --reason "exact signed reason"
no-slop axi run --intent "the work to validate"
```

The next matching dispatch carries the signed reason and adjudication ID in
`runs.dispatch_reason` and `runs.cancel_adjudication_id`. Re-registering a
consumed envelope does not make it usable again. Concurrent matching dispatches
can consume a grant at most once. Changing hosts/stores requires transferring
the authoritative store and its history; a new empty store is not a refund.

`runs.waste_usage_json` is the terminal usage producer contract, schema 1, for
failed and cancelled runs. It contains exact run/lane/repository/branch identity,
status, `terminal_at_ms`, and `turns`. Each turn retains its invocation ID,
step/purpose/round, observed provider/model, per-attempt token deltas, nullable
estimated and known-subtotal cost, price-source SHA-256/path/status, cost basis,
exit status, and observation clocks. Review/fix/test turns can be selected by
purpose and step. Other purposes remain visible so total failed work is not
underreported. A durable pending attempt is written before calling the agent;
unreported fields stay null if the daemon dies. Completion replaces that same
attempt, and a late completion refreshes the same terminal row.

The existing Quartermaster table is read from `MO_MODEL_LEDGER`, defaulting to
`~/.config/mo/quartermaster/model-ledger.json`. Exact observed provider plus
model key/model_id selects a row. Numeric `input_per_mtok`, `output_per_mtok`,
optional `cache_read_per_mtok` and `cache_write_per_mtok`, and `cost_class` are
used. Missing/invalid/ambiguous rates or identities remain unknown. Nonzero
cache usage with no cache rate makes the total unknown; the measured, priced
subtotal is separate. Raw cumulative cache-creation usage on resumed sessions
is not charged again. `list_imputed` means estimated API-equivalent consumption,
not a payment. `free` is kept distinct. A missing provider table does not erase
usage and does not become zero cost.

The hourly emitter is owned by Master-Orchestrator item 10 / #571. Its reader
selects `[window_start*1000, window_end*1000)` on `terminal_at_ms`, deduplicates by
(store, run ID), and groups cost by basis/provider/model. It must not sum partial
subtotals as complete totals or count a late refreshed run twice. A schema-1
row with no turns means no recorded invocations; legacy/missing payloads remain
unknown. This producer adds no hourly line or timer.
