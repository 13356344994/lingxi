# Equity Session Gap — Official Source Fitness

> Document hash: `f7aa3102cfdfcab4a770ea4a100f999960e3545f5010cd2bd9d8f7a05a7110d8`
> JSON artifact: `equity_session_gap_source_fitness.json`
> JSON hash (`hashes.source_fitness_hash`): `3c88d6aa189be2393b9c84aa6313b544dcde82f3e6ce8908b0bb328887990c3b`
> Bound shortlist hash (recomputed): `b3c63aee84509eead9dc24bf067e68cdc16f08f50a532e006a68e633b015c541`
> Bound reviewer receipt hash (recomputed): `6c5a0c25f065e169f64f909aacedd4fc85f7ca0dad066222cc5e3f146a2bbdce`
> Calendar JSON hash: `65e7795a7a68d0384ddad15188d8c8b2818df225d042197b4d12ee7a51086297`
> Route: `equity_perp_underlying_session_gap_forced_repricing` (FAB-MSV2-01 rescoped)
> Owner: Grok 4.5 (official source/mechanism data owner)
> Produced at: `2026-07-14T06:35:00Z`

## Verdicts

| Item | Adjudication |
| --- | --- |
| calendar | **PRESENT** |
| mark_index_method | **PRESENT_SUPPORTS** |
| symbol_mapping | **PARTIAL** |
| **source_fitness** | **READY_FOR_LOCAL_FEATURE_TEST** |

Outcome-blind: no pnl/return/winner fields read; no price-result judgment.

## 1) US session calendar
Official NYSE Holidays & Trading Hours + Nasdaq holiday schedule / Nasdaq Trader calendar agree for 2026 regular hours, 10 full holidays, and 2 early closes. Constructed `us_market_calendar_2026.json` with America/New_York sessions, UTC open/close, DST flags, KnownFrom, and per-source content hashes.

## 2) OKX stock/ETF perpetual official method (closed session)
Primary page: https://www.okx.com/help/stock-perpetuals (fetch sha256 `e02edcd2…855e9c`, 2026-07-14T06:20:33Z).

Evidence summary (official text, not generic-perp inference):
- 24/7 trading; US regular cash hours cited as 09:30–16:00 ET.
- Index constituents (types): stock feeds (Pyth/Ondo), other venues’ stock perps, OKX contract price, other exchanges’ index prices.
- **Off-hours/weekends/holidays:** index **relies more** on OKX contract price and other stock-perp indices (not frozen).
- **Index bands:** ±10% vs vendor stock price; when cash market closed, **last available stock price** used for the **band**, preventing abnormal deviation — this is a band, **not** a peg/freeze of index/mark to last print.
- Mark = Index + basis MA (stock page states same as regular perps; linked mark doc cited only as referenced).
- Funding / price limits: “standard” with per-stock adjustable parameters; stock-specific closed-session funding freeze **not** documented → partial / ABSENT_UNVERIFIED where noted in JSON.
- OI prose rules: **ABSENT_UNVERIFIED** in docs; live instrument limit fields only.

Anti-pattern avoided: generic perpetual mark formula alone was **not** treated as the stock closed-session method.

## 3) Symbol mapping OKX → NYSE/Nasdaq underlying
**PARTIAL.**
- PRESENT: ticker-string identity via official naming (`[TICKER]USDT`) and instruments fields `instId`/`uly`/`ctValCcy`; live index-components use `AAPL/USD` etc.
- ABSENT: listing venue (NYSE vs Nasdaq), MIC/ISIN/CUSIP, legal name.
- Famous-ticker common knowledge was **not** used.

## 4) Stop-gate → unique fitness verdict
Methodology falsification cut #3 (**frozen/clamped to last cash print**) → **NOT_TRIGGERED**.  
Calendar for 2026 → constructible.  
→ **READY_FOR_LOCAL_FEATURE_TEST** (not DATA_BLIND / UNEXECUTABLE / MECHANISM_FALSIFIED).

Open for later local work: cut #2 feature separability; official listing-map for complete US venue-bound universe (mapping PARTIAL).

## Decision Journal
1. Recomputed binding hashes — MATCH.
2. Required dual-venue official calendars — written.
3. Stock Perpetuals page authoritative for closed-session index — PRESENT_SUPPORTS.
4. ±10% band ≠ freeze/peg — not FALSIFIED.
5. Mapping without venue fields — PARTIAL.
6. Unique verdict READY_FOR_LOCAL_FEATURE_TEST.
7. Rejected third-party calendars / 403 ICE IR page.

## Safety
NO_GO / no trade / no commit / write-scope limited to the four allowed new artifacts.
