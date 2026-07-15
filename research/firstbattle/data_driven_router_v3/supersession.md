# V3 Supersession — Isolate Mechanism-Search-V2 Priors (`supersede-mechanism-priors`)

> Document hash: `b24e26398b8082b4fbd9895bb7ef834fedc195b79127905c09888241ead812dc`
> JSON artifact: `supersession.json` (`hashes.supersession_hash = 44f098e19cffa0657a963673b5c97425dcac58dcf15ae7d5939a8fec9ae7bad2`)
> Generation: `firstbattle-data-driven-router-v3` · Todo: `supersede-mechanism-priors`
> Reviewed repo HEAD: `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Status: `MECHANISM_PRIORS_SUPERSEDED_ISOLATED_AS_HISTORICAL_HYPOTHESIS_EVIDENCE`
> `labels_unblinded=false`, `routes_started=0`, `routes_reopened=0`, `exam_actions=0`, `NO_GO`, `trade_allowed=false`, no commit.

This artifact executes only the V3 todo `supersede-mechanism-priors`. Allowed write set: this file, `supersession.json`, and `FIRST_BATTLE_MASTER_PLAN.md`. Nothing else was modified: no plan file, no mechanism-search-v2 artifact, no code, no `records/`, no private raw, no G1–G10 frozen protocol, no exam/gold paper, no real-money switch. No commit was made.

## Supreme objective — preserved verbatim

> LINGXI 永久、唯一、最高优先级目标：在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

V3 changes only the search *method* (data-driven state–action learning), never the objective. The Master's section 1 text is left byte-identical.

## What is superseded, what is inherited

- **Superseded (as prior admission):** payer identity, institutional exclusion, event sparsity, small-capacity, no-speed-dependence, and moat. These no longer gate which routes may exist. They may re-appear only (a) as raw decision-time-visible variables measurable from PIT data, or (b) as a **post-validation** economic explanation attached *after* a state–action region already passed independent validation/WF — never reaching back to change discovery, selection, or thresholds.
- **Inherited (evidence-trust only):** DataTruth (three clocks / PIT / completeness / de-dup / fail-closed), PIT & KnownFrom, time isolation (purge/embargo, non-overlapping discovery/validation/WF), complete cost (real fees/slippage/funding/unfilled/delay; unknowns flagged and stress-tested, never zero-filled), account safety (`global_halt`/`NO_GO`/`EXAM_UNAVAILABLE`/`trade_allowed=false`, 5× hard cap, independent solvency, no `records/` mutation, no real money without explicit user approval), and the frozen historical **failure conclusions** of G1–G10 and mechanism-search-v2. These are inherited only so that any historical evidence V3 uses is trustworthy; they are not routes and grant no trading right, and no frozen verdict is reopened or renamed.

## Mechanism-search-v2 = historical hypothesis evidence only

Status for **every** file under `research/firstbattle/mechanism_search_v2/`: `HYPOTHESIS_ONLY_NOT_MODEL_INPUT`. This covers all candidate proposals (`candidates_opus/fable/grok`), the `mechanism_hypothesis_library`, the `mechanism_shortlist` and its reviewer receipt, and the equity session-gap `source_fitness`, the `us_market_calendar_2026`, and the equity session-gap `data_contract`. None of them may enter V3 state vectors, features, labels, reward, candidate generation, or path selection. **This exclusion holds regardless of any artifact's declared or recomputed hash.** Self-declared reference hashes for each file are recorded in `supersession.json` under `exclude_from_v3_model_input`.

## Old equity 3-symbol contract — disposed without adjudication

`research/firstbattle/mechanism_search_v2/equity_session_gap_data_contract.{json,md}`:

- **Scope:** `SUPERSEDED_SCOPE_REJECTED` — the user rejected the scope of pre-deciding which route/universe may exist (a fixed 3-symbol equity session-gap frame driven by mapping/moat/capacity priors).
- **Hash:** `HASH_INTEGRITY_DISPUTED_NOT_TRUSTED` — a Terra Go recompute reports the repository `datatruth.HashJSON` value as `1618af25…f3ac982`, conflicting with the declared `47e4113ea9cb2aaffc7d3bdeace2a43997917ea837e5f6fcf71302c6b3c6213a`; a separate owner declaration asserts the declared `47e4113e…` value reproduces under an independent Python implementation. Because a Terra report and an owner declaration conflict, this artifact **does not self-adjudicate** and **does not modify** the disputed files.
- **V3 effect:** regardless of which hash is correct, this contract is **not** a V3 input, and neither hash may be used as any validity or admission gate.

Non-adjudicating corroboration (see DJ7): an independent Python reproduction of `datatruth.HashJSON` matched three controls exactly (`mechanism_shortlist` `b3c63aee`, `mechanism_shortlist_reviewer_receipt` `6c5a0c25`, `candidates_opus` `1ed962fc`) but differed from `mechanism_hypothesis_library`'s declared `d208f96c` (Python `55cec3aa`) **solely** because that file stores integer-valued floats (e.g. `6.0`) that Go renders as `6` and Python as `6.0`. This shows a Python-vs-Go numeric-serialization gap can by itself produce exactly this kind of declared-vs-recomputed divergence, which is why the equity contract hash is marked *disputed and untrusted* rather than resolved in favor of either value.

## Old data mainline & P0 Data Fitness = data-quality/cost evidence only (not route labels)

`data_mainline_closure_v1.{json,md}` and `p0_swap_data_fitness{,_measurement,_reviewer_receipt}.{json,md}` may be consulted by V3 **only** as evidence about data quality, coverage, identity, denominator completeness and realized execution cost (to build honest reward/cost and OOD gates). Their `DATA_BLIND` / `routes_reopened=0` conclusions are data-quality statements, **not** market verdicts and **not** route labels. Reference hashes are recorded in `supersession.json` under `include_as_v3_evidence`.

## Forbidden imports into the V3 dataset

The full list is in `supersession.json` (`forbidden_imports_into_v3_dataset`). In short: no candidate IDs/rankings, no library scoring/weights/scores, no shortlist selection or the single conditional equity route, no equity session-gap route/payer/boundary/calendar/universe framing as a label, no disputed contract hash as a gate, no subjective admission prior as a pre-search filter, and no mechanism-search-v2 / G1–G10 winner/loser/outcome as a V3 label or reward.

## Current action & state

Current action switches to **`firstbattle-data-driven-router-v3`**. All safety gates hold: `labels_unblinded=false`, `routes_started=0`, `routes_reopened=0`, `exam_actions=0`, `system_status=NO_GO`, `exam_status=EXAM_UNAVAILABLE`, `trade_allowed=false`, `global_halt=true`. This artifact freezes only the supersede boundary; it does not freeze the V3 protocol, build the dataset, run the model tournament, validate, replay, or deliver a verdict — each is a later V3 todo with its own gate.

## Hash & verification methods

- **JSON**: `hashes.supersession_hash` set to null, then `datatruth.HashJSON` (deterministic Go `encoding/json` + SHA-256 lowercase hex). Numeric literals restricted to safe small integers so the value is serializer-independent. Self-hash `44f098e19cffa0657a963673b5c97425dcac58dcf15ae7d5939a8fec9ae7bad2` reproduces with the repository's Go implementation.
- **Markdown**: normalize the `> Document hash:` line to `` `SELF_EXCLUDED` ``, then SHA-256 over UTF-8 LF bytes.
