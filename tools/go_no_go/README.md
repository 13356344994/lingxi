# Phase -1 Go/No-Go (Task 5.6) — Offline

Independent **offline** script to answer: after conservative costs, is single-venue positive-funding spot-perp on Binance majors worth engineering?

**No network access.** Reads cached JSON from `data/` only.

## Quick start

```bash
cd /root/lingxi/tools/go_no_go
python3.11 analyze.py   # requires Python 3.10+
```

Outputs:

- `RESULT.md` — human-readable summary + GO/NO-GO
- `report.json` — full machine-readable report

Data must already exist under `data/<SYMBOL>.json` (10 majors, ~90 days).

## Strategy modeled (v0)

**Single-venue positive-funding cash-and-carry:** buy spot `N` + short perp `N` at 2× leverage.

### Hold rules

1. **Primary (selective):** hold iff the *previous* 8h settlement funding rate was positive; otherwise flat. Entry/exit at settlement boundaries with full round-trip costs per cycle.
2. **Comparison (always hold):** hold the entire window with one open+close cycle.

### PnL components

```
net = Σ(discrete funding at held settlements, spike-adjusted)
    + (entry_basis − exit_basis)
    − fees − slippage
```

- **Discrete funding:** only settlements actually held through; no hourly extrapolation.
- **Basis:** `(mark − spot) / spot` at entry/exit using `spot_8h_close` and `markPrice`.
- **Spike haircut:** `|rate|` above per-symbol 90-day p90 → only 50% of excess acknowledged.

## Hard-coded thresholds (`config.py`)

| Parameter | Value |
|-----------|-------|
| Lookback | 90 days |
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

## GO criteria (all required)

1. Universe weighted net annualized ≥ 45% on occupied capital (selective hold)
2. ≥ 5 symbols with positive conservative edge
3. Spot fee +5 bps scenario still positive (universe level)
4. No liquidation at ±20%/±30% price shock (isolated perp wallet model)
5. Top-2 coins ≤ 70% of positive edge
6. ≥ 1,000 USDT/year net at 4,400 USDT deployable capital
7. Three 30-day regime segments all non-negative (risk-adjusted net ann ≥ 0)

Any failure → **NO-GO**. Do not tune thresholds after seeing results.

## Sensitivity output

- Spot fee: base −5 / base / base +5 bps
- Slippage: 0 / 2 / 5 / 10 bps per leg

## Dependencies

Standard library only. See `requirements.txt`.

## Data format

Each `data/<SYMBOL>.json`:

```json
{
  "symbol": "BTCUSDT",
  "days": 90,
  "funding": [{"fundingTime": <ms>, "fundingRate": "<str>", "markPrice": "<str>"}],
  "spot_8h_close": {"<openTimeMs>": <float>, ...}
}
```

`data/_summary.json` has gross funding overview per symbol.

## Note on fetch scripts

`fetch.py` / `fetch_binance.py` exist for one-time data collection. **`analyze.py` does not call them.**
