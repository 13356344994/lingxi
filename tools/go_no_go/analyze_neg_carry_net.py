"""Full NET negative-carry (LONG perp + SHORT spot) across data_broad.

Selective hold: enter when previous 8h settlement funding < 0, exit when >= 0.
Subtracts borrow interest, fees/slippage, and tracks basis PnL (opposite sign vs
positive-carry cash-and-carry in models.simulate_symbol). Offline only."""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from config import CONSERVATIVE, MIN_NET_ANNUALIZED_PCT, OCCUPIED_CAPITAL_RATIO
from models import (
    Settlement,
    basis_bps,
    effective_funding_rate,
    load_symbol_json,
    percentile,
    round_trip_cost_bps,
)

ROOT = Path(__file__).resolve().parent
BROAD_DIR = ROOT / "data_broad"
BORROW_PATH = ROOT / "borrow_rates.json"

# OCCUPIED_CAPITAL_RATIO (1.6x) matches positive-carry analysis: spot N + perp
# margin + buffer. Real margin collateral for borrowed shorts may be higher
# (often 1.5-2x coin notional), so annualized returns here are optimistic.


@dataclass
class NegCarryRow:
    symbol: str
    base_asset: str
    vol_m: float
    borrowable: bool
    borrow_annual_pct: float
    borrow_limit: float
    borrow_limit_usd: float
    latest_spot: float
    days: float
    settlements_total: int
    settlements_held: int
    position_cycles: int
    captured_funding_bps: float
    borrow_cost_bps: float
    fee_cost_bps: float
    basis_bps: float
    net_bps: float
    net_ann_pct: float
    net_ann_no_basis_pct: float


def base_asset_from_symbol(symbol: str) -> str:
    if symbol.endswith("USDT"):
        return symbol[:-4]
    return symbol


def latest_spot_price(payload: dict) -> float:
    spot_map = payload.get("spot_8h_close") or {}
    if not spot_map:
        return 0.0
    latest_ts = max(int(k) for k in spot_map)
    return float(spot_map[str(latest_ts)])


def simulate_neg_carry(
    symbol: str,
    settlements: list[Settlement],
    daily_borrow_rate: float = 0.0,
) -> tuple[int, int, int, float, float, float, float, float]:
    """Returns cycles, held, captured_funding_bps, borrow_cost_bps, fee_cost_bps, basis_bps, days."""
    if len(settlements) < 2:
        return 0, 0, 0, 0.0, 0.0, 0.0, 0.0, 0.0

    abs_rates = [abs(s.funding_rate) for s in settlements]
    spike_threshold = percentile(abs_rates, 90)

    days = max(
        (settlements[-1].funding_time - settlements[0].funding_time) / 86_400_000.0,
        1.0,
    )

    holding = False
    entry_basis = 0.0
    captured_funding = 0.0
    held_count = 0
    cycles = 0
    basis_pnl = 0.0
    rt_cost = round_trip_cost_bps(CONSERVATIVE)

    def open_position(at: Settlement) -> None:
        nonlocal holding, entry_basis, cycles
        holding = True
        entry_basis = basis_bps(at.spot_close, at.mark_price)
        cycles += 1

    def close_position(at: Settlement) -> None:
        nonlocal holding, basis_pnl
        if not holding:
            return
        exit_basis = basis_bps(at.spot_close, at.mark_price)
        # Long perp + short spot profits when (mark-spot) widens (exit_basis > entry_basis).
        # Opposite of positive-carry models.simulate_symbol which uses entry - exit.
        basis_pnl += exit_basis - entry_basis
        holding = False

    for idx, s in enumerate(settlements):
        if idx == 0:
            continue
        prev_negative = settlements[idx - 1].funding_rate < 0
        if prev_negative and not holding:
            open_position(settlements[idx - 1])
        elif not prev_negative and holding:
            close_position(settlements[idx - 1])

        if holding:
            eff = effective_funding_rate(s.funding_rate, spike_threshold)
            captured_funding += -eff
            held_count += 1

    if holding:
        close_position(settlements[-1])

    captured_funding_bps = captured_funding * 10_000.0
    held_days = (held_count * 8.0) / 24.0
    borrow_cost_bps = daily_borrow_rate * held_days * 10_000.0
    fee_cost_bps = cycles * rt_cost

    return (
        cycles,
        held_count,
        len(settlements),
        captured_funding_bps,
        borrow_cost_bps,
        fee_cost_bps,
        basis_pnl,
        days,
    )


def annualize(net_bps: float, days: float) -> float:
    return (net_bps / 10_000.0) / OCCUPIED_CAPITAL_RATIO * (365.0 / days) * 100.0


def load_borrow_rates() -> dict[str, dict]:
    with BORROW_PATH.open() as f:
        return json.load(f)


def analyze_symbol(path: Path, borrow_rates: dict[str, dict]) -> NegCarryRow | None:
    try:
        with path.open() as f:
            payload = json.load(f)
        symbol, settlements = load_symbol_json(path)
    except Exception:
        return None

    if len(settlements) < 10:
        return None

    base = base_asset_from_symbol(symbol)
    borrow = borrow_rates.get(base)
    borrowable = borrow is not None
    daily_rate = float(borrow["daily_rate"]) if borrowable else 0.0
    borrow_annual = float(borrow["annual_pct"]) if borrowable else 0.0
    borrow_limit = float(borrow["borrow_limit"]) if borrowable else 0.0

    spot = latest_spot_price(payload)
    borrow_limit_usd = borrow_limit * spot if spot > 0 else 0.0
    vol_m = float(payload.get("quote_volume_24h", 0.0)) / 1e6

    (
        cycles,
        held,
        total,
        cap_bps,
        borrow_bps,
        fee_bps,
        basis_bps_val,
        days,
    ) = simulate_neg_carry(symbol, settlements, daily_borrow_rate=daily_rate)

    net_no_basis_bps = cap_bps - borrow_bps - fee_bps
    net_bps = net_no_basis_bps + basis_bps_val

    return NegCarryRow(
        symbol=symbol,
        base_asset=base,
        vol_m=vol_m,
        borrowable=borrowable,
        borrow_annual_pct=borrow_annual,
        borrow_limit=borrow_limit,
        borrow_limit_usd=borrow_limit_usd,
        latest_spot=spot,
        days=days,
        settlements_total=total,
        settlements_held=held,
        position_cycles=cycles,
        captured_funding_bps=cap_bps,
        borrow_cost_bps=borrow_bps,
        fee_cost_bps=fee_bps,
        basis_bps=basis_bps_val,
        net_bps=net_bps,
        net_ann_pct=annualize(net_bps, days),
        net_ann_no_basis_pct=annualize(net_no_basis_bps, days),
    )


def main() -> None:
    borrow_rates = load_borrow_rates()
    rows: list[NegCarryRow] = []

    for fp in sorted(BROAD_DIR.glob("*.json")):
        if fp.name.startswith("_"):
            continue
        row = analyze_symbol(fp, borrow_rates)
        if row is not None:
            rows.append(row)

    rows.sort(key=lambda r: r.net_ann_pct, reverse=True)

    hdr = (
        f"{'symbol':<12}{'vol$M':>7}{'borrow%/yr':>11}{'borrow_lim$':>12}"
        f"{'held/tot':>10}{'capFund':>9}{'borrow':>8}{'fees':>7}{'basis':>8}"
        f"{'netbps':>8}{'netAnn%':>9}{'noBasis%':>9}{'note':>14}"
    )
    print("\n=== Negative-carry NET (LONG perp + SHORT spot, selective hold) ===")
    print(hdr)
    print("-" * len(hdr))

    for r in rows:
        note = "" if r.borrowable else "not_borrowable"
        blim = f"{r.borrow_limit_usd/1e3:.0f}k" if r.borrow_limit_usd >= 1000 else f"{r.borrow_limit_usd:.0f}"
        if not r.borrowable:
            blim = "-"
            borrow_yr = "-"
        else:
            borrow_yr = f"{r.borrow_annual_pct:.1f}"

        print(
            f"{r.symbol:<12}{r.vol_m:>7.1f}{borrow_yr:>11}{blim:>12}"
            f"{r.settlements_held:>4}/{r.settlements_total:<5}"
            f"{r.captured_funding_bps:>9.1f}{r.borrow_cost_bps:>8.1f}{r.fee_cost_bps:>7.1f}"
            f"{r.basis_bps:>8.1f}{r.net_bps:>8.1f}{r.net_ann_pct:>9.2f}"
            f"{r.net_ann_no_basis_pct:>9.2f}{note:>14}"
        )

    n = len(rows)
    borrowable = [r for r in rows if r.borrowable]
    nb = len(borrowable)
    pos_incl = [r for r in borrowable if r.net_ann_pct > 0]
    pos_no_basis = [r for r in borrowable if r.net_ann_no_basis_pct > 0]
    go_incl = [r for r in borrowable if r.net_ann_pct >= MIN_NET_ANNUALIZED_PCT]
    go_no_basis = [r for r in borrowable if r.net_ann_no_basis_pct >= MIN_NET_ANNUALIZED_PCT]

    ew_incl = sum(r.net_ann_pct for r in borrowable) / nb if nb else 0.0
    ew_no_basis = sum(r.net_ann_no_basis_pct for r in borrowable) / nb if nb else 0.0

    not_borrowable = [r for r in rows if not r.borrowable]

    print(f"\n=== Summary ===")
    print(f"Total coins analyzed: {n}")
    print(f"Not borrowable (excluded from tradeable verdict): {len(not_borrowable)}")
    if not_borrowable:
        print(f"  -> {', '.join(r.symbol for r in not_borrowable)}")
    print(f"Borrowable coins: {nb}")
    print(f"Net-positive (incl basis), borrowable: {len(pos_incl)}/{nb}")
    print(f"Net-positive (no basis), borrowable: {len(pos_no_basis)}/{nb}")
    print(f"Clear +{MIN_NET_ANNUALIZED_PCT:.0f}% GO threshold (incl basis), borrowable: {len(go_incl)}/{nb}")
    if go_incl:
        print(f"  -> {', '.join(r.symbol for r in go_incl)}")
    print(f"Clear +{MIN_NET_ANNUALIZED_PCT:.0f}% GO threshold (no basis), borrowable: {len(go_no_basis)}/{nb}")
    print(f"Equal-weight net ann (borrowable, incl basis): {ew_incl:.2f}%")
    print(f"Equal-weight net ann (borrowable, no basis): {ew_no_basis:.2f}%")
    print(f"Occupied-capital ratio: {OCCUPIED_CAPITAL_RATIO}x (optimistic for margin shorts)")

    print("\n=== Tradeability caveats ===")
    print(
        "Borrow limits are in COIN units; USD cap above uses latest spot_8h_close. "
        "Many small-cap negative-funding names have borrow caps of only $1k–$50k, "
        "far below commercial scale."
    )
    thin_borrow = sorted(
        [r for r in borrowable if r.borrow_limit_usd > 0],
        key=lambda r: r.borrow_limit_usd,
    )[:8]
    if thin_borrow:
        print("Smallest USD borrow caps (borrowable coins):")
        for r in thin_borrow:
            print(
                f"  {r.symbol}: ~${r.borrow_limit_usd:,.0f} "
                f"({r.borrow_limit:,.0f} {r.base_asset} @ ${r.latest_spot:.4f})"
            )
    print(
        "Juiciest negative-funding coins are often dumping small-caps with short-squeeze "
        "and borrow-rate-spike risk. VIP0 borrow rates here are point-in-time averages; "
        "actual borrow cost during negative-funding windows is often much higher."
    )


if __name__ == "__main__":
    main()
