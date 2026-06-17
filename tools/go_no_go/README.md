# Phase -1 Go/No-Go — Offline Analysis

Independent **offline** scripts to answer: after conservative costs, are funding-carry structures worth engineering at commercial scale?

**No network access** for analysis scripts. Reads cached JSON from `data/` and `data_broad/` only. Fetch scripts require network (run from parent agent).

## Quick start

```bash
cd /root/lingxi/tools/go_no_go

# Narrow sample: 10 majors, 90 days
python3.11 analyze.py

# Broad sample: 65 spot+perp dual-listed coins, 180 days
python3.11 analyze_broad.py

# Negative carry (gross)
python3.11 analyze_neg_carry.py

# Negative carry (net, subtract borrow from borrow_rates.json)
python3.11 analyze_neg_carry_net.py
```

Outputs:

- `RESULT.md` — human-readable summary + GO/NO-GO (from `analyze.py` / `run.py`)
- `report.json` — full machine-readable report (`analyze.py`)
- Broad/neg scripts print to stdout (pipe to file if archiving)

## Scripts

| Script | Data | Purpose |
|--------|------|---------|
| `analyze.py` | `data/*.json` (10 majors, ~90d) | Primary GO/NO-GO gate; writes `RESULT.md` |
| `analyze_broad.py` | `data_broad/*.json` (65 coins, ~180d) | Wide-universe positive carry; liquidity tiers |
| `analyze_neg_carry.py` | `data_broad/` | Negative carry gross (no borrow) |
| `analyze_neg_carry_net.py` | `data_broad/` + `borrow_rates.json` | Negative carry net after borrow interest |
| `fetch_binance.py` | — | One-time narrow fetch (network) |
| `fetch_binance_broad.py` | — | One-time broad fetch (network) |
| `run.py` | — | Wrapper that runs `analyze.py` and refreshes `RESULT.md` |

## Strategy modeled (v0)

**Single-venue positive-funding cash-and-carry:** buy spot `N` + short perp `N` at 2× leverage.

Negative-carry scripts model **long perp + short spot (borrow)** with selective hold when previous settlement rate &lt; 0.

### Hold rules (positive carry)

1. **Primary (selective):** hold iff the *previous* 8h settlement funding rate was positive; otherwise flat. Entry/exit at settlement boundaries with full round-trip costs per cycle.
2. **Comparison (always hold):** hold the entire window with one open+close cycle.

### PnL components

```
net = Σ(discrete funding at held settlements, spike-adjusted)
    + (entry_basis − exit_basis)
    − fees − slippage
    [− borrow interest for neg-carry net scripts]
```

- **Discrete funding:** only settlements actually held through; no hourly extrapolation.
- **Basis:** `(mark − spot) / spot` at entry/exit using `spot_8h_close` and `markPrice`.
- **Spike haircut:** `|rate|` above per-symbol 90-day p90 → only 50% of excess acknowledged.

## Hard-coded thresholds (`config.py`)

| Parameter | Value |
|-----------|-------|
| Lookback | 90 days (narrow) / 180 days (broad) |
| GO net annualized (occupied capital) | ≥ 45% |
| Min qualifying symbols | ≥ 5 |
| Spot taker fee | 10 bps (5 actual + 5 conservative) |
| Perp taker fee | 5 bps |
| Slippage | 5 bps per leg (base) |
| Occupied capital | 1.6× notional (spot + ½ margin + 10% buffer) |
| Perp leverage | 2× |
| Commercial capital | 4,400 USDT |
| Commercial min net | 1,000 USDT/year |
| Capital tiers | 440 / 2,200 / 4,400 USDT |

## GO criteria (`analyze.py` — all required)

1. Universe weighted net annualized ≥ 45% on occupied capital (selective hold)
2. ≥ 5 symbols with positive conservative edge
3. Spot fee +5 bps scenario still positive (universe level)
4. No liquidation at ±20%/±30% price shock (isolated perp wallet model)
5. Top-2 coins ≤ 70% of positive edge
6. ≥ 1,000 USDT/year net at 4,400 USDT deployable capital
7. Three 30-day regime segments all non-negative (risk-adjusted net ann ≥ 0)

Any failure → **NO-GO**. Do not tune thresholds after seeing results.

## Documented results (2026-06-17/18, offline)

| Analysis | Verdict | Headline |
|----------|---------|----------|
| Narrow 10 majors (`analyze.py`) | **NO-GO** | Net ann **−58%** on occupied capital; 0/10 positive |
| Broad 65 coins (`analyze_broad.py`) | **NO-GO** | Equal-weight net ann **−59.85%**; 7/65 positive |
| Neg carry net (`analyze_neg_carry_net.py`) | **NO-GO** at portfolio level | Equal-weight **−53%**; only 5 micro-caps clear +45% |
| Live perp-perp sample | factual | 181 trades, ~**1.6 USDT** total funding |

User chose to continue building the **shadow** unified rotation system despite NO-GO. Do not lower the 45% bar to claim GO.

## Sensitivity output (`analyze.py`)

- Spot fee: base −5 / base / base +5 bps
- Slippage: 0 / 2 / 5 / 10 bps per leg

## Dependencies

Standard library only. See `requirements.txt`.

## Data format

Each `data/<SYMBOL>.json` or `data_broad/<SYMBOL>.json`:

```json
{
  "symbol": "BTCUSDT",
  "days": 90,
  "funding": [{"fundingTime": <ms>, "fundingRate": "<str>", "markPrice": "<str>"}],
  "spot_8h_close": {"<openTimeMs>": <float>, ...}
}
```

`data/_summary.json` / `data_broad/_summary.json` have gross funding overview per symbol.

`borrow_rates.json` caches VIP0 hourly borrow rates for negative-carry net analysis.

## Note on fetch scripts

`fetch.py` / `fetch_binance.py` / `fetch_binance_broad.py` exist for one-time data collection. **Analysis scripts do not call them.**
