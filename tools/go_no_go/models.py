"""EV / PnL models: discrete funding, basis, costs, margin stress."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Literal

from config import (
    CONSERVATIVE,
    OCCUPIED_CAPITAL_RATIO,
    PEAK_FUNDING_HAIRCUT,
    PERP_LEVERAGE,
    PERP_MAINTENANCE_MARGIN_RATE,
    PERP_WALLET_BUFFER_RATIO,
    PRICE_SHOCK_PCTS,
    SPIKE_PERCENTILE,
    CostScenario,
)

HoldRule = Literal["selective", "always"]


@dataclass
class Settlement:
    funding_time: int
    funding_rate: float
    mark_price: float
    spot_close: float


@dataclass
class SymbolResult:
    symbol: str
    hold_rule: str
    days: float
    settlements_total: int
    settlements_held: int
    position_cycles: int
    raw_funding_bps: float
    effective_funding_bps: float
    basis_pnl_bps: float
    fee_cost_bps: float
    slippage_cost_bps: float
    total_cost_bps: float
    net_return_bps: float
    net_annualized_pct: float
    qualifies: bool
    spike_p90_abs_rate: float
    liquidation_at_shock: dict[str, bool] = field(default_factory=dict)


def percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    if len(ordered) == 1:
        return ordered[0]
    rank = (pct / 100.0) * (len(ordered) - 1)
    lo = int(rank)
    hi = min(lo + 1, len(ordered) - 1)
    weight = rank - lo
    return ordered[lo] * (1.0 - weight) + ordered[hi] * weight


def effective_funding_rate(rate: float, spike_threshold: float) -> float:
    """Spike haircut: only 50% of |rate| excess above historical p90 |rate|."""
    abs_rate = abs(rate)
    if abs_rate <= spike_threshold:
        return rate
    excess = abs_rate - spike_threshold
    adjusted_abs = spike_threshold + PEAK_FUNDING_HAIRCUT * excess
    return adjusted_abs if rate >= 0 else -adjusted_abs


def leg_cost_bps(is_spot: bool, scenario: CostScenario) -> float:
    fee = scenario.spot_fee_bps if is_spot else scenario.perp_fee_bps
    return fee + scenario.slippage_bps


def round_trip_cost_bps(scenario: CostScenario) -> float:
    """One full open+close cycle: spot buy+sell + perp open+close."""
    return 2 * leg_cost_bps(True, scenario) + 2 * leg_cost_bps(False, scenario)


def perp_wallet_liquidated(shock_pct: float, notional: float = 1.0) -> bool:
    initial_margin = notional / PERP_LEVERAGE
    buffer = PERP_WALLET_BUFFER_RATIO * notional
    maintenance = PERP_MAINTENANCE_MARGIN_RATE * notional
    equity = initial_margin + buffer - shock_pct * notional
    return equity < maintenance


def basis_bps(spot: float, mark: float) -> float:
    if spot <= 0:
        return 0.0
    return (mark / spot - 1.0) * 10_000.0


EIGHT_H_MS = 8 * 3_600_000


def funding_to_spot_key(funding_time: int) -> int:
    """Map funding settlement ms to 8h kline openTime (00/08/16 UTC)."""
    return (funding_time // EIGHT_H_MS) * EIGHT_H_MS


def load_symbol_json(path: Path) -> tuple[str, list[Settlement]]:
    with path.open() as f:
        payload = json.load(f)

    symbol = payload["symbol"]
    spot_map = {int(k): float(v) for k, v in payload["spot_8h_close"].items()}
    settlements: list[Settlement] = []

    for row in payload["funding"]:
        ft = int(row["fundingTime"])
        spot_key = funding_to_spot_key(ft)
        spot_close = spot_map.get(spot_key)
        if spot_close is None:
            continue
        settlements.append(
            Settlement(
                funding_time=ft,
                funding_rate=float(row["fundingRate"]),
                mark_price=float(row["markPrice"]),
                spot_close=spot_close,
            )
        )

    settlements.sort(key=lambda s: s.funding_time)
    return symbol, settlements


def slice_settlements(
    settlements: list[Settlement],
    start_ms: int | None = None,
    end_ms: int | None = None,
) -> list[Settlement]:
    if not settlements:
        return []
    start = start_ms if start_ms is not None else settlements[0].funding_time
    end = end_ms if end_ms is not None else settlements[-1].funding_time
    return [s for s in settlements if start <= s.funding_time <= end]


def simulate_symbol(
    symbol: str,
    settlements: list[Settlement],
    scenario: CostScenario = CONSERVATIVE,
    hold_rule: HoldRule = "selective",
    spike_threshold: float | None = None,
) -> SymbolResult:
    if len(settlements) < 2:
        return _empty_result(symbol, hold_rule)

    abs_rates = [abs(s.funding_rate) for s in settlements]
    p90 = percentile(abs_rates, SPIKE_PERCENTILE)
    if spike_threshold is None:
        spike_threshold = p90

    days = max(
        (settlements[-1].funding_time - settlements[0].funding_time) / 86_400_000.0,
        1.0,
    )

    holding = False
    entry_basis = 0.0
    raw_funding = 0.0
    eff_funding = 0.0
    held_count = 0
    cycles = 0
    fee_cost = 0.0
    slip_cost = 0.0
    basis_pnl = 0.0

    spot_leg = scenario.spot_fee_bps
    perp_leg = scenario.perp_fee_bps
    slip_leg = scenario.slippage_bps

    def open_position(at: Settlement) -> None:
        nonlocal holding, entry_basis, cycles, fee_cost, slip_cost
        holding = True
        entry_basis = basis_bps(at.spot_close, at.mark_price)
        cycles += 1
        fee_cost += spot_leg + perp_leg
        slip_cost += 2 * slip_leg

    def close_position(at: Settlement) -> None:
        nonlocal holding, basis_pnl, fee_cost, slip_cost
        if not holding:
            return
        exit_basis = basis_bps(at.spot_close, at.mark_price)
        basis_pnl += entry_basis - exit_basis
        holding = False
        fee_cost += spot_leg + perp_leg
        slip_cost += 2 * slip_leg

    if hold_rule == "always":
        open_position(settlements[0])
        for s in settlements:
            raw_funding += s.funding_rate
            eff = effective_funding_rate(s.funding_rate, spike_threshold)
            eff_funding += eff
            held_count += 1
        close_position(settlements[-1])
    else:
        # Selective: hold iff previous settlement rate was positive.
        for idx, s in enumerate(settlements):
            if idx == 0:
                continue
            prev_positive = settlements[idx - 1].funding_rate > 0
            if prev_positive and not holding:
                open_position(settlements[idx - 1])
            elif not prev_positive and holding:
                close_position(settlements[idx - 1])

            if holding:
                raw_funding += s.funding_rate
                eff = effective_funding_rate(s.funding_rate, spike_threshold)
                eff_funding += eff
                held_count += 1

        if holding:
            close_position(settlements[-1])

    funding_bps = eff_funding * 10_000.0
    total_cost = fee_cost + slip_cost
    net_bps = funding_bps + basis_pnl - total_cost
    net_ann = (net_bps / 10_000.0) / OCCUPIED_CAPITAL_RATIO * (365.0 / days) * 100.0

    liq = {f"{int(pct * 100)}%": perp_wallet_liquidated(pct) for pct in PRICE_SHOCK_PCTS}

    return SymbolResult(
        symbol=symbol,
        hold_rule=hold_rule,
        days=days,
        settlements_total=len(settlements),
        settlements_held=held_count,
        position_cycles=cycles,
        raw_funding_bps=raw_funding * 10_000.0,
        effective_funding_bps=funding_bps,
        basis_pnl_bps=basis_pnl,
        fee_cost_bps=fee_cost,
        slippage_cost_bps=slip_cost,
        total_cost_bps=total_cost,
        net_return_bps=net_bps,
        net_annualized_pct=net_ann,
        qualifies=net_ann > 0 and held_count >= 3,
        spike_p90_abs_rate=p90,
        liquidation_at_shock=liq,
    )


def _empty_result(symbol: str, hold_rule: str) -> SymbolResult:
    return SymbolResult(
        symbol=symbol,
        hold_rule=hold_rule,
        days=0.0,
        settlements_total=0,
        settlements_held=0,
        position_cycles=0,
        raw_funding_bps=0.0,
        effective_funding_bps=0.0,
        basis_pnl_bps=0.0,
        fee_cost_bps=0.0,
        slippage_cost_bps=0.0,
        total_cost_bps=0.0,
        net_return_bps=0.0,
        net_annualized_pct=0.0,
        qualifies=False,
        spike_p90_abs_rate=0.0,
    )


def portfolio_metrics(results: list[SymbolResult], capital_usdt: float) -> dict:
    if not results:
        return {
            "weighted_net_annualized_pct": 0.0,
            "expected_net_usdt_year": 0.0,
            "edge_contributions": [],
            "top2_contribution_pct": 0.0,
            "qualifying_count": 0,
        }

    n = len(results)
    weighted_ann = sum(r.net_annualized_pct for r in results) / n

    notional_per_symbol = (capital_usdt / n) / OCCUPIED_CAPITAL_RATIO
    net_usdt_year = 0.0
    for r in results:
        period_return = r.net_return_bps / 10_000.0
        net_usdt_year += notional_per_symbol * period_return * (365.0 / max(r.days, 1.0))

    total_edge = sum(max(r.net_return_bps, 0.0) for r in results)
    contributions = []
    for r in results:
        share = (max(r.net_return_bps, 0.0) / total_edge * 100.0) if total_edge > 0 else 0.0
        contributions.append(
            {
                "symbol": r.symbol,
                "edge_share_pct": share,
                "net_annualized_pct": r.net_annualized_pct,
                "net_return_bps": r.net_return_bps,
            }
        )
    contributions.sort(key=lambda x: x["edge_share_pct"], reverse=True)
    top2 = sum(c["edge_share_pct"] for c in contributions[:2])

    return {
        "weighted_net_annualized_pct": weighted_ann,
        "expected_net_usdt_year": net_usdt_year,
        "notional_per_symbol": notional_per_symbol,
        "edge_contributions": contributions,
        "top2_contribution_pct": top2,
        "qualifying_count": sum(1 for r in results if r.qualifies),
    }


def result_to_dict(result: SymbolResult) -> dict:
    return asdict(result)
