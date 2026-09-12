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
requires an independent signed, single-use adjudication.

Admission is one transaction and it runs first. Reserving the budget, consuming
the grant, creating the run and identifying the runs this dispatch supersedes
all happen together, before any cancellation side effect. That ordering is the
guarantee: a dispatch the budget refuses leaves the lane's active run exactly as
it was, leaves the one-use grant unconsumed, and leaves no run row behind. A
refused dispatch spends no provider turn and destroys no authorized gate. Only
after the transaction commits are the superseded runs it named actually
cancelled.

The operator provisions a base64 Ed25519 public key at
`<NS_HOME>/cancel-budget-public-key`. Its private key stays with the independent
coordinator. There is no key creation, reset, waive, or self-sign command.

The file's location is not authority, because the store root is caller-selected
and a lane with store write access could replace the bytes. The key's identity
must be bound to authority the lane cannot rewrite, and registration refuses
otherwise:

* **Operator-owned path.** The key file and its directory are owned by root (or
  by any uid other than the invoking user) and are not group- or world-writable.
* **Installed policy fingerprint.** `cancel-budget-policy.json`, deployed beside
  the running executable and never inside the store, pins
  `public_key_sha256`. The key must hash to it exactly. The policy file itself
  must not be group- or world-writable.

`cancel-adjudication` prints `adjudicator_key_authority` naming which binding
admitted the key.

Dispatch is also bound to one store. `NS_HOME` selects which database holds the
abort history, so a caller free to name a fresh store resets the budget by
choosing an empty one. The dispatcher refuses any `axi run` aimed at a store
other than the declared authoritative one (`~/.fleet/mo/gate-dispatch/authoritative-store`,
defaulting to `~/.no-mistakes`) rather than silently redirecting it.

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
underreported.

Every concrete attempt is durable before it runs. One row is opened per adapter
attempt - retries and fallback-provider attempts included - and written before
the provider is invoked, so an attempt the process never returns from still has
a row and an invocation ID, with `exit_status` left at `running` and unreported
fields null. The first reported attempt reuses the row opened for the
invocation, so a single-attempt turn is one row, not two. Completion replaces
that same attempt by ID, and a late completion refreshes the same terminal row.

The existing Quartermaster table is read from `MO_MODEL_LEDGER` or
`QM_MODEL_LEDGER` - the overrides MO's own price reader and the schema owner
already use - defaulting to `~/.config/mo/quartermaster/model-ledger.json`. The document is validated exactly as
`tools/fleet/quartermaster.py` validates it: version 1, `generated_at`, a closed
row field set, the closed `cost_class` vocabulary, class/price agreement, and no
duplicate key or ambiguous selector. A document that owner would refuse yields
no price here either. Exact observed provider plus model key/model_id selects a
row. The authoritative table carries no cache rates, so nonzero cache usage on a
priced row leaves the total unknown while the measured, priced subtotal stays
separate. Raw cumulative cache-creation usage on resumed sessions is not charged
again. `list_imputed` means estimated API-equivalent consumption, not a payment.
A `free` or `local` row bills nothing for any token class, so its cost is an
exact zero rather than unknown. A missing provider table does not erase usage and
does not become zero cost.

The hourly emitter is owned by Master-Orchestrator item 10 / #571. Its reader
selects `[window_start*1000, window_end*1000)` on `terminal_at_ms`, deduplicates by
(store, run ID), and groups cost by basis/provider/model. It must not sum partial
subtotals as complete totals or count a late refreshed run twice. A schema-1
row with no turns means no recorded invocations; legacy/missing payloads remain
unknown. This producer adds no hourly line or timer.
