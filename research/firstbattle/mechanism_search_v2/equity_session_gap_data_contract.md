# Equity Perp Underlying Session-Gap — Data / Feature Falsification Contract

> Document hash: `7d076643f30d27e5e07a93e44c6c40940760ad83cd97956e9bf714b85691d61e`
> JSON artifact: `equity_session_gap_data_contract.json`
> JSON hash (`hashes.contract_hash`): `47e4113ea9cb2aaffc7d3bdeace2a43997917ea837e5f6fcf71302c6b3c6213a`
> Route: `equity_perp_underlying_session_gap_forced_repricing` (source candidate FAB-MSV2-01)
> Status: `FROZEN_DATA_CONTRACT_DEFINITION_STOPPED_BEFORE_ANY_OUTCOME_READ`
> `routes_started=0`, `labels_unblinded=false`, no download, no outcome read, no commit.

This freezes the minimal data/feature falsification contract for the shortlisted route **before reading any future return/outcome**. It does not download, does not start the route, and makes no profit/direction judgment. It inherits the root gate, clocks, 50/20/10 support gate, tiers, forbidden fields and stop-gate semantics of `route_data_contracts.json`.

## Independent hash recompute (all bindings)

All upstream binding hashes were **independently recomputed** with the repository `datatruth.HashJSON` semantics (Go `encoding/json` deterministic encoding: sorted keys, compact, HTML-escape `<` `>` `&` and U+2028/U+2029, UTF-8, SHA-256 lowercase hex, self-field nulled). The Python reproduction was first validated against 4 known declared hashes before being trusted.

| Artifact | Self-excluded field | Declared | Recomputed | Match |
| --- | --- | --- | --- | --- |
| mechanism_shortlist.json | hashes.shortlist_hash | b3c63aee…15c541 | b3c63aee…15c541 | ✅ |
| mechanism_shortlist_reviewer_receipt.json | hashes.receipt_hash | 6c5a0c25…a2bbdce | 6c5a0c25…a2bbdce | ✅ |
| equity_session_gap_source_fitness.json | hashes.source_fitness_hash | 3c88d6aa…990c3b | 3c88d6aa…990c3b | ✅ |
| us_market_calendar_2026.json | hashes.calendar_hash | 65e7795a…086297 | 65e7795a…086297 | ✅ |
| route_data_contracts.json | hashes.protocol_hash | 4a4c201d…c453588 | 4a4c201d…c453588 | ✅ |
| route_data_source_registry.json | hashes.registry_hash | 1c667256…2e5318 | 1c667256…2e5318 | ✅ |
| history_catalog.json | data_hash | f41ed258…16379c | cross-checked = route contract binding (not re-scanned) | ➖ |

`history_catalog.data_hash` is a tape-content scan hash (not a datatruth self-hash); re-scanning all tape is out of a GET-only contract owner's budget, so it is cross-checked equal to the already-frozen `route_data_contracts.json` binding.

## Scope (strict)

- **Universe = exactly the 3 symbols OKX's official Stock Perpetuals page exemplifies**: `AAPL-USDT-SWAP`, `TSLA-USDT-SWAP`, `NVDA-USDT-SWAP` (help page: "e.g. AAPL, TSLA, NVDA" / "AAPLUSDT, TSLAUSDT, NVDAUSDT"). **Deliberately narrower** than the shortlist's GME/MSFT/SPY/QQQ/IWM enumeration list. No common-knowledge SPY/QQQ expansion, no claim of a complete stock-perp universe, no unverified venue/ISIN mapping (symbol mapping remains **PARTIAL**).
- **Calendar** = official 2026 NYSE/Nasdaq artifact coverage only (both venues fetched independently and agree). All local tape is inside 2026, so coverage is sufficient; pre-2026 would be DATA_BLIND (no as-of calendar).
- **Events** = per symbol × cash `{regular_open, regular_close, long_close, long_open}`; same-day symbols cluster by `day_block`/`shock_block`. Independence is bounded by trading-day and shock-block counts, not raw event counts.

## Existing local data vs the one real gap

Already local (no new acquisition, 0 bad parse rows): OKX **last-price** candles `okx_1h` (AAPL/NVDA 2026-03-04→07-09 ~127d; TSLA 2026-02-25→07-09 ~134d), `okx_5m` (all ~2026-05-10→07-09 ~60d), `okx_funding` (~2026-04-07→07-09, 8h). Public crowding proxies (rubik LS-ratio / taker flow / bybit funding) are present but **forbidden as decision features**.

**The only gap**: local tape is last-price only. **Index candles and mark candles are absent** — required for the pre-open perp-index basis and the mark-freeze check. This is the sole minimal new GET-only acquisition.

## Minimal new acquisition (define-before-download; not executed)

- `GET /api/v5/market/history-index-candles?instId={AAPL|TSLA|NVDA}-USDT` — official index candles → pre-open basis.
- `GET /api/v5/market/history-mark-price-candles?instId={AAPL|TSLA|NVDA}-USDT-SWAP` — official mark candles → mark-freeze check.
- **Minimal range**: 1H aligned to existing last-price windows (spine); 5m only over the 2026-05-10→07-09 sub-window (precision overlay). No forward/future fetch, no other symbols.
- **GET-only**, public, no auth. Est. ≈186 GET (1H) / +1038 (5m overlay). **Disk hard cap 200 MB** (expected <15 MB) → `DISK_CAP_EXCEEDED` stops.
- **raw/manifest/PIT**: append-only raw with `fetched_at`; per-file manifest (first/last ts, expected/present bars, gap list, dup count, sha256, KnownFrom); methodology KnownFrom = 2026-07-14T06:20:31Z, marked **DOCUMENTED_CURRENT / PIT_UNPROVEN_PRE_FETCH** — the current webpage cannot backfill the method version onto pre-KnownFrom historical bars.

## Cut 1 — Full-subset closure movement & coverage (pre-Phase-A)

For all covered closed windows (close_utc → next open_utc: overnight, weekend, holiday) on all 3 symbols, using bars completed **before** the boundary (no cross-boundary return → outcome-blind), verify the perp is **not systematically frozen**, data is complete, and non-zero volume/price-change ratios hold.

**Thresholds frozen a priori** from OKX's documented 24/7 continuous-trading guarantee (not value-tuned): `coverage ≥ 0.95`, `nonzero_volume_ratio ≥ 0.90`, `intrabar_move_ratio ≥ 0.80`.

**Failure classification**: systemic sub-threshold movement/volume **or** mark frozen/synthetically pegged → `MECHANISM_FALSIFIED_FROZEN` (route dies); per-window coverage miss → `DATA_INCOMPLETE` (exclude, don't fabricate); systemic coverage miss / no index-mark coverage → `DATA_BLIND`.

**Outcome-blind pre-freeze observation** (run by this owner on local last-price 1H bars, thresholds fixed first):

| Symbol | Closed windows | Coverage | Nonzero vol | Intrabar move | Verdict |
| --- | --- | --- | --- | --- | --- |
| AAPL | 86 | 1.000 | 0.996 | 0.990 | PASS |
| NVDA | 87 | 1.000 | 1.000 | 0.999 | PASS |
| TSLA | 92 | 1.000 | 1.000 | 1.000 | PASS |

Last-price layer PASSES decisively (generalizes the reviewer's single AAPL/Saturday sample). The **mark layer stays OPEN** pending mark-candle fetch.

## Cut 2 — Boundary-feature identifiability (pre-Phase-A)

**DecisionTime** = the last complete bar before regular open (bar_close_utc ≤ open_utc; 1H → ~30 min slack, 5m → 0–5 min slack; per-event resolution tier recorded).

**Features (DecisionTime-visible only)**: closed-window cumulative perp move, pre-open perp-index basis (needs index candle; drop if unavailable, never fabricate), closed-window range, closed-window volume, closed-window funding (if available). Public crowding factors and any post-boundary return are **forbidden**.

**Matched controls** must be outcome-blind, same symbol/session/month/duration, and **must not cross a regular-open boundary** (MID_CLOSED_WINDOW and IN_SESSION_PLACEBO families). **Time-label trap guard**: "different clock time" does not count as separable — controls are time-of-day matched (or time-of-day is blocked/stratified), so any residual separability must come from mechanism features.

**Frozen criteria** (prove features are constructible & non-degenerate only — **not** direction/profit): constructibility coverage ≥ 0.95 per last-price feature; non-degeneracy (non-constant, pairwise |Spearman| < 0.99, not collapsing to a public factor); time-controlled distinguishability (permutation test N=10000, Bonferroni α=0.01, |Cliff's delta| ≥ 0.147); ≥ 50 valid matched pairs. Failure → `UNIDENTIFIABLE` / `ISOMORPH_PUBLIC_FACTOR` / `DATA_BLIND` / `INSUFFICIENT_SUPPORT_FOR_CUT2`.

## Potential support counts (common window 2026-03-04→07-09, outcome-blind)

- Independent trading days: **88** (> 20 ✅) · pooled regular-open events: **264** (> 50 ✅) · distinct weekend/holiday shock blocks: **18** (> 10 ✅).
- Per symbol: AAPL/NVDA 88 trading days (18 long boundaries), TSLA 93 (19). `PLAUSIBLE_MEETS_50_20_10`, but independence is bounded by 88 days / 18 shock blocks, not 264 events. A 5m-only sub-window (~42 days, ~8–9 shock blocks) would **fail** the 10-block floor.

## Data designs (≥3) and selection

| Design | Summary | Decision |
| --- | --- | --- |
| D1 1H-only full window | Max independence, coarse DecisionTime (30 min) | REJECTED as sole (kept as spine) |
| D2 5m-only sub-window | Precise DecisionTime, fails 10-block floor | REJECTED as sole |
| **D3 Hybrid tiered (1H spine + 5m overlay)** | 1H clears 50/20/10 over full window; 5m adds precision over 60d; per-event resolution tier | **CHOSEN** |
| D4 last-price-only proxy basis | No index/mark fetch, proxy basis | REJECTED (proxy can't impersonate index; no mark check) |
| D5 bulk full-market | Bulk L2/trades | REJECTED (bulk forbidden, out of scope) |

## Frozen: identity, priority, clocks, budget, stop gates

- **Event identity**: `(instId, boundary_type, session_date_ET, boundary_utc)`; long_open/long_close are subset flags, not extra events.
- **Source priority**: tier0 official OKX candles (last-price local; index/mark GET-only) + OKX docs (methodology, KnownFrom) + official calendar; tier3 funding; forbidden = snapshot PIT backfill, K-line-as-index, crowding-as-direction, paid/bulk.
- **Clocks/PIT**: event=venue candle ts, receive=fetched_at, process=write ts; features ≤ DecisionTime, labels after (forbidden); methodology PIT_UNPROVEN_PRE_FETCH.
- **Missing/duplicate/gap**: dedup by (instId, bar_ts); missing → per-window DATA_INCOMPLETE; boundary-crossing gap → event QUALITY_INVALID (excluded).
- **Institutional moat: UNPROVEN** (crypto-native MMs not excluded; OI-cap capacity argument unverified).
- **Budget**: 1 CPU / 6 h / 1 attempt; GET-only, zero purchase/bulk.
- **Stop gates**: index/mark endpoints can't cover the local window → **DATA_BLIND stop**; mark frozen/pegged → MECHANISM_FALSIFIED_FROZEN; Cut-2 not time-controlled-distinguishable → UNIDENTIFIABLE; direction collapses to public factor → ISOMORPH_PUBLIC_FACTOR; unverifiable holiday/DST sub-period → exclude; disk > 200 MB → stop; any binding hash mismatch → fail closed. Any trigger stops the current generation immediately (earliest-blocker classification).

## Self-hash / drift

JSON self-hash excludes `hashes.contract_hash`; this MD's hash excludes only its own `> Document hash:` line (one-directional, so no JSON↔MD cycle). JSON/MD must stay consistent on route_id, universe, chosen_design, potential counts, cut thresholds and stop gates, else it is drift requiring a new generation. `git diff --check` clean; no commit performed.
