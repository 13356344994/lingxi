# Phase -1 Go/No-Go Result (v0 offline)

- **Run at (UTC):** 2026-06-17T13:38:12.903809+00:00
- **Strategy:** single_venue_spot_perp_positive_carry @ binance
- **Lookback:** 90 days (local cache)
- **Verdict:** **NO-GO**

> Mainstream coin cash-and-carry edge too thin after conservative costs; failed: net_annual_gte_45pct, min_5_qualifying, spot_plus_5bps_not_negative, commercial_scale, three_regimes_stable.

## Methodology

- **Primary hold rule:** selective: hold iff previous 8h settlement funding rate > 0; enter/exit at settlement boundaries with full round-trip costs
- **Comparison:** always: hold entire window, one open+close cycle
- **Occupied capital ratio:** 1.6× notional
- **Discrete funding:** only at 8h settlements actually held through
- **Spike haircut:** 50% of |rate| above per-symbol 90-day p90 |rate|

### Cost assumptions (conservative)

| Item | Value |
|------|-------|
| Spot taker | 10 bps |
| Perp taker | 5 bps |
| Slippage / leg | 5 bps |
| Full round-trip | 50 bps |

## Summary

- Weighted net annualized (occupied capital, selective hold): **-58.23%**
- Per-symbol range: **-121.33%** – **-29.52%**
- Expected net USDT/year @ 4400U: **-2561.98**
- Qualifying symbols (positive edge): **0** — none

## Per-Symbol (selective hold)

| Symbol | Held/Total | Cycles | Funding (bps) | Basis (bps) | Cost (bps) | Net (bps) | Net Ann % |
|--------|------------|--------|---------------|-------------|------------|-----------|-----------|
| AVAXUSDT | 166/270 | 47 | 53.4 | 1136.4 | 2350.0 | -1160.3 | -29.52 |
| BNBUSDT | 119/270 | 44 | 35.9 | 953.6 | 2200.0 | -1210.5 | -30.80 |
| ETHUSDT | 150/270 | 31 | 46.3 | -95.4 | 1550.0 | -1599.1 | -40.68 |
| SOLUSDT | 112/270 | 43 | 16.9 | 341.4 | 2150.0 | -1791.7 | -45.58 |
| DOGEUSDT | 177/270 | 45 | 67.4 | -122.0 | 2250.0 | -2304.6 | -58.63 |
| XRPUSDT | 119/270 | 51 | 11.5 | 188.7 | 2550.0 | -2349.8 | -59.78 |
| BTCUSDT | 151/270 | 35 | 36.2 | -674.6 | 1750.0 | -2388.4 | -60.76 |
| LINKUSDT | 205/270 | 43 | 83.1 | -524.9 | 2150.0 | -2591.8 | -65.94 |
| ADAUSDT | 155/270 | 53 | 52.2 | -123.6 | 2650.0 | -2721.5 | -69.24 |
| SUIUSDT | 179/270 | 54 | 47.2 | -2116.0 | 2700.0 | -4768.9 | -121.33 |

## Per-Symbol (always hold — comparison)

| Symbol | Funding (bps) | Basis (bps) | Cost (bps) | Net (bps) | Net Ann % |
|--------|---------------|-------------|------------|-----------|-----------|
| BNBUSDT | 53.1 | 26.9 | 50.0 | 30.0 | 0.76 |
| DOGEUSDT | 68.1 | -159.3 | 50.0 | -141.2 | -3.59 |
| AVAXUSDT | 45.3 | -184.2 | 50.0 | -188.9 | -4.81 |
| BTCUSDT | 9.9 | -155.2 | 50.0 | -195.3 | -4.97 |
| LINKUSDT | 98.3 | -256.8 | 50.0 | -208.5 | -5.30 |
| SUIUSDT | 53.1 | -225.5 | 50.0 | -222.4 | -5.66 |
| XRPUSDT | -22.4 | -171.7 | 50.0 | -244.1 | -6.21 |
| ADAUSDT | 30.9 | -232.6 | 50.0 | -251.7 | -6.40 |
| ETHUSDT | 12.8 | -240.2 | 50.0 | -277.4 | -7.06 |
| SOLUSDT | -56.8 | -278.3 | 50.0 | -385.1 | -9.80 |

## Edge Contribution

- BTCUSDT: 0.0% of positive edge (-60.76% ann.)
- ETHUSDT: 0.0% of positive edge (-40.68% ann.)
- SOLUSDT: 0.0% of positive edge (-45.58% ann.)
- BNBUSDT: 0.0% of positive edge (-30.80% ann.)
- XRPUSDT: 0.0% of positive edge (-59.78% ann.)
- DOGEUSDT: 0.0% of positive edge (-58.63% ann.)
- ADAUSDT: 0.0% of positive edge (-69.24% ann.)
- AVAXUSDT: 0.0% of positive edge (-29.52% ann.)
- LINKUSDT: 0.0% of positive edge (-65.94% ann.)
- SUIUSDT: 0.0% of positive edge (-121.33% ann.)
- Top-2 concentration: **0.0%**

## Capital Tiers (expected net USDT/year, selective hold)

- **440U:** -256.20 USDT/yr (-58.23% ann. on occupied capital)
- **2200U:** -1280.99 USDT/yr (-58.23% ann. on occupied capital)
- **4400U:** -2561.98 USDT/yr (-58.23% ann. on occupied capital)

_Capacity assumption (v0): capital split equally across all loaded symbols; no depth/slippage scaling by size._

## Sensitivity (spot fee ±5bps, slippage)

| Δspot bps | slippage bps | Net Ann % | Net USDT/yr @4400U |
|-----------|--------------|-----------|-------------------|
| -5 | 0 | -24.19 | -1064.19 |
| -5 | 2 | -33.26 | -1463.60 |
| -5 | 5 | -46.88 | -2062.72 |
| -5 | 10 | -69.57 | -3061.24 |
| +0 | 0 | -35.53 | -1563.45 |
| +0 | 2 | -44.61 | -1962.86 |
| +0 | 5 | -58.23 | -2561.98 |
| +0 | 10 | -80.92 | -3560.50 |
| +5 | 0 | -46.88 | -2062.72 |
| +5 | 2 | -55.96 | -2462.13 |
| +5 | 5 | -69.57 | -3061.24 |
| +5 | 10 | -92.27 | -4059.77 |

## Three Regime Segments (30d each)

- **early_30d**: ann=-80.82%, net=-3556.05 USDT/yr, qualifying=1 — ❌ negative
- **middle_30d**: ann=-73.71%, net=-3243.09 USDT/yr, qualifying=1 — ❌ negative
- **recent_30d**: ann=-34.38%, net=-1512.68 USDT/yr, qualifying=1 — ❌ negative

## GO Checks (all required)

- ❌ `net_annual_gte_45pct`: False
- ❌ `min_5_qualifying`: False
- ❌ `spot_plus_5bps_not_negative`: False
- ✅ `no_liquidation_at_shock`: True
- ✅ `not_top2_dominated`: True
- ❌ `commercial_scale`: False
- ❌ `three_regimes_stable`: False

## Recommendation

**Stop allocator / multi-executor build.** Archive as research tool.

Mainstream coins' current-regime single-venue positive-funding cash-and-carry edge is too thin after conservative costs (fees, slippage, basis, turnover) to justify building a unified funding opportunity system.
