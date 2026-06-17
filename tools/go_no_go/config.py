"""Hard-coded thresholds for phase -1 go/no-go. DO NOT tune after seeing results."""

from __future__ import annotations

from dataclasses import dataclass

# --- Universe (Binance majors, spot + USDT-M perp dual-listed) ---
V0_SYMBOLS: tuple[str, ...] = (
    "BTCUSDT",
    "ETHUSDT",
    "SOLUSDT",
    "BNBUSDT",
    "XRPUSDT",
    "DOGEUSDT",
    "ADAUSDT",
    "AVAXUSDT",
    "LINKUSDT",
    "SUIUSDT",
)

LOOKBACK_DAYS = 90
REGIME_SEGMENT_DAYS = 30

# --- Fees (conservative, fixed before run) ---
# Spot taker = 10 bps (5 bps actual + 5 bps conservative buffer)
SPOT_TAKER_FEE_BPS = 10.0
# Perp taker = 5 bps
PERP_TAKER_FEE_BPS = 5.0
# Slippage per leg (base scenario)
CONSERVATIVE_SLIPPAGE_BPS = 5.0

# Sensitivity grids (doc §成本敏感性)
SPOT_FEE_SENSITIVITY_BPS = (-5.0, 0.0, 5.0)  # relative to base spot fee
SLIPPAGE_SENSITIVITY_BPS = (0.0, 2.0, 5.0, 10.0)

# --- Funding spike haircut ---
PEAK_FUNDING_HAIRCUT = 0.50  # only acknowledge 50% of excess above p90 |rate|
SPIKE_PERCENTILE = 90

# --- Capital / leverage ---
PERP_LEVERAGE = 2.0
PERP_MAINTENANCE_MARGIN_RATE = 0.005  # 0.5% of notional (majors, conservative)
PERP_WALLET_BUFFER_RATIO = 0.10  # 10% of notional kept in futures wallet

# Occupied capital = spot N + perp margin (N/2) + 10% buffer = 1.6 N
OCCUPIED_CAPITAL_RATIO = 1.0 + (1.0 / PERP_LEVERAGE) + PERP_WALLET_BUFFER_RATIO

# --- GO / NO-GO gates (written before run) ---
MIN_NET_ANNUALIZED_PCT = 45.0  # on occupied capital, 3x safety vs 15% target
MIN_QUALIFYING_SYMBOLS = 5
MAX_TOP2_CONTRIBUTION_PCT = 70.0
COMMERCIAL_CAPITAL_USDT = 4400.0
COMMERCIAL_MIN_NET_USDT_YEAR = 1000.0
CAPITAL_TIERS_USDT = (440.0, 2200.0, 4400.0)

# Price shock stress (% instant move against short perp leg)
PRICE_SHOCK_PCTS = (0.20, 0.30)


@dataclass(frozen=True)
class CostScenario:
    name: str
    spot_fee_bps: float
    perp_fee_bps: float
    slippage_bps: float


CONSERVATIVE = CostScenario(
    name="conservative",
    spot_fee_bps=SPOT_TAKER_FEE_BPS,
    perp_fee_bps=PERP_TAKER_FEE_BPS,
    slippage_bps=CONSERVATIVE_SLIPPAGE_BPS,
)
