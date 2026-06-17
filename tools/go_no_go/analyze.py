"""Phase -1 go/no-go analysis — offline only, reads local data/."""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

from config import (
    CAPITAL_TIERS_USDT,
    COMMERCIAL_CAPITAL_USDT,
    COMMERCIAL_MIN_NET_USDT_YEAR,
    CONSERVATIVE,
    CONSERVATIVE_SLIPPAGE_BPS,
    LOOKBACK_DAYS,
    MAX_TOP2_CONTRIBUTION_PCT,
    MIN_NET_ANNUALIZED_PCT,
    MIN_QUALIFYING_SYMBOLS,
    OCCUPIED_CAPITAL_RATIO,
    REGIME_SEGMENT_DAYS,
    SPOT_FEE_SENSITIVITY_BPS,
    SPOT_TAKER_FEE_BPS,
    SLIPPAGE_SENSITIVITY_BPS,
    V0_SYMBOLS,
    CostScenario,
)
from models import (
    load_symbol_json,
    portfolio_metrics,
    result_to_dict,
    simulate_symbol,
    slice_settlements,
)

DATA_DIR = Path(__file__).resolve().parent / "data"
HOLD_RULE_PRIMARY = "selective"
HOLD_RULE_COMPARE = "always"


def load_all_symbols(symbols: tuple[str, ...] = V0_SYMBOLS) -> dict[str, list]:
    loaded: dict[str, list] = {}
    issues: list[str] = []
    for sym in symbols:
        path = DATA_DIR / f"{sym}.json"
        if not path.exists():
            issues.append(f"{sym}: missing {path}")
            continue
        try:
            _, settlements = load_symbol_json(path)
            if len(settlements) < 10:
                issues.append(f"{sym}: only {len(settlements)} settlements")
                continue
            loaded[sym] = settlements
        except Exception as exc:  # noqa: BLE001
            issues.append(f"{sym}: {exc}")
    return loaded, issues


def regime_windows(settlements: list) -> dict[str, tuple[int, int]]:
    if not settlements:
        return {}
    start = settlements[0].funding_time
    end = settlements[-1].funding_time
    seg_ms = REGIME_SEGMENT_DAYS * 86_400_000
    return {
        "early_30d": (start, start + seg_ms),
        "middle_30d": (start + seg_ms, start + 2 * seg_ms),
        "recent_30d": (start + 2 * seg_ms, end),
    }


def eval_universe(
    symbol_data: dict[str, list],
    scenario: CostScenario,
    hold_rule: str = HOLD_RULE_PRIMARY,
    window: tuple[int, int] | None = None,
) -> list:
    results = []
    for sym, settlements in symbol_data.items():
        windowed = settlements
        if window is not None:
            windowed = slice_settlements(settlements, window[0], window[1])
        if len(windowed) < 2:
            continue
        results.append(simulate_symbol(sym, windowed, scenario=scenario, hold_rule=hold_rule))
    return results


def sensitivity_rows(symbol_data: dict[str, list], hold_rule: str = HOLD_RULE_PRIMARY) -> list[dict]:
    rows: list[dict] = []
    for sf in SPOT_FEE_SENSITIVITY_BPS:
        for sl in SLIPPAGE_SENSITIVITY_BPS:
            scenario = CostScenario(
                name=f"spot{sf:+.0f}_sl{sl:.0f}",
                spot_fee_bps=SPOT_TAKER_FEE_BPS + sf,
                perp_fee_bps=CONSERVATIVE.perp_fee_bps,
                slippage_bps=sl,
            )
            results = eval_universe(symbol_data, scenario, hold_rule=hold_rule)
            pm = portfolio_metrics(results, COMMERCIAL_CAPITAL_USDT)
            rows.append(
                {
                    "spot_fee_delta_bps": sf,
                    "slippage_bps": sl,
                    "weighted_net_annualized_pct": pm["weighted_net_annualized_pct"],
                    "expected_net_usdt_year": pm["expected_net_usdt_year"],
                }
            )
    return rows


def run_analysis() -> dict:
    symbol_data, data_issues = load_all_symbols()
    now = datetime.now(timezone.utc).isoformat()

    primary = eval_universe(symbol_data, CONSERVATIVE, hold_rule=HOLD_RULE_PRIMARY)
    compare_always = eval_universe(symbol_data, CONSERVATIVE, hold_rule=HOLD_RULE_COMPARE)

    commercial = portfolio_metrics(primary, COMMERCIAL_CAPITAL_USDT)
    qualifying = [r.symbol for r in primary if r.qualifies]

    capital_tiers = {
        str(int(cap)): portfolio_metrics(primary, cap) for cap in CAPITAL_TIERS_USDT
    }

    # Spot +5bps sensitivity (GO gate #3)
    spot_plus_5 = CostScenario(
        name="spot_plus_5",
        spot_fee_bps=SPOT_TAKER_FEE_BPS + 5.0,
        perp_fee_bps=CONSERVATIVE.perp_fee_bps,
        slippage_bps=CONSERVATIVE.slippage_bps,
    )
    spot_plus_5_results = eval_universe(symbol_data, spot_plus_5, hold_rule=HOLD_RULE_PRIMARY)
    spot_plus_5_pm = portfolio_metrics(spot_plus_5_results, COMMERCIAL_CAPITAL_USDT)

    sensitivity = sensitivity_rows(symbol_data, hold_rule=HOLD_RULE_PRIMARY)

    # Three regime segments
    sample_sym = next(iter(symbol_data.values()), [])
    windows = regime_windows(sample_sym)
    regimes: dict[str, dict] = {}
    all_regimes_ok = True
    for name, window in windows.items():
        rs = eval_universe(symbol_data, CONSERVATIVE, hold_rule=HOLD_RULE_PRIMARY, window=window)
        pm = portfolio_metrics(rs, COMMERCIAL_CAPITAL_USDT)
        stable = pm["weighted_net_annualized_pct"] >= 0
        all_regimes_ok = all_regimes_ok and stable
        regimes[name] = {
            "window_start_ms": window[0],
            "window_end_ms": window[1],
            "weighted_net_annualized_pct": pm["weighted_net_annualized_pct"],
            "expected_net_usdt_year": pm["expected_net_usdt_year"],
            "qualifying_count": pm["qualifying_count"],
            "stable": stable,
            "per_symbol": [
                {
                    "symbol": r.symbol,
                    "net_annualized_pct": r.net_annualized_pct,
                    "net_return_bps": r.net_return_bps,
                }
                for r in sorted(rs, key=lambda x: x.net_annualized_pct, reverse=True)
            ],
        }

    checks = {
        "net_annual_gte_45pct": commercial["weighted_net_annualized_pct"] >= MIN_NET_ANNUALIZED_PCT,
        "min_5_qualifying": len(qualifying) >= MIN_QUALIFYING_SYMBOLS,
        "spot_plus_5bps_not_negative": spot_plus_5_pm["weighted_net_annualized_pct"] > 0,
        "no_liquidation_at_shock": all(
            not any(r.liquidation_at_shock.values()) for r in primary
        ),
        "not_top2_dominated": commercial["top2_contribution_pct"] <= MAX_TOP2_CONTRIBUTION_PCT,
        "commercial_scale": commercial["expected_net_usdt_year"] >= COMMERCIAL_MIN_NET_USDT_YEAR,
        "three_regimes_stable": all_regimes_ok,
    }
    go = all(checks.values())

    failed = [k for k, v in checks.items() if not v]

    return {
        "version": "v0-offline",
        "strategy": "single_venue_spot_perp_positive_carry",
        "venue": "binance",
        "run_at_utc": now,
        "lookback_days": LOOKBACK_DAYS,
        "hold_rule_primary": (
            "selective: hold iff previous 8h settlement funding rate > 0; "
            "enter/exit at settlement boundaries with full round-trip costs"
        ),
        "hold_rule_compare": "always: hold entire window, one open+close cycle",
        "occupied_capital_ratio": OCCUPIED_CAPITAL_RATIO,
        "cost_assumptions": {
            "spot_taker_bps": CONSERVATIVE.spot_fee_bps,
            "perp_taker_bps": CONSERVATIVE.perp_fee_bps,
            "slippage_bps_per_leg": CONSERVATIVE.slippage_bps,
            "round_trip_cost_bps": 2 * (CONSERVATIVE.spot_fee_bps + CONSERVATIVE.slippage_bps)
            + 2 * (CONSERVATIVE.perp_fee_bps + CONSERVATIVE.slippage_bps),
            "spike_haircut": "50% of |rate| above per-symbol 90-day p90 |rate|",
        },
        "symbols_loaded": list(symbol_data.keys()),
        "data_issues": data_issues,
        "per_symbol_selective": [result_to_dict(r) for r in primary],
        "per_symbol_always_hold": [result_to_dict(r) for r in compare_always],
        "qualifying_symbols": qualifying,
        "portfolio_commercial": commercial,
        "capital_tiers": capital_tiers,
        "sensitivity": sensitivity,
        "regimes": regimes,
        "spot_plus_5bps": spot_plus_5_pm,
        "go_checks": checks,
        "failed_checks": failed,
        "verdict": "GO" if go else "NO-GO",
        "verdict_reason": (
            "All GO criteria satisfied."
            if go
            else "Mainstream coin cash-and-carry edge too thin after conservative costs; "
            f"failed: {', '.join(failed)}."
        ),
    }


def render_markdown(report: dict) -> str:
    lines = [
        "# Phase -1 Go/No-Go Result (v0 offline)",
        "",
        f"- **Run at (UTC):** {report['run_at_utc']}",
        f"- **Strategy:** {report['strategy']} @ {report['venue']}",
        f"- **Lookback:** {report['lookback_days']} days (local cache)",
        f"- **Verdict:** **{report['verdict']}**",
        "",
        f"> {report['verdict_reason']}",
        "",
        "## Methodology",
        "",
        f"- **Primary hold rule:** {report['hold_rule_primary']}",
        f"- **Comparison:** {report['hold_rule_compare']}",
        f"- **Occupied capital ratio:** {report['occupied_capital_ratio']:.1f}× notional",
        f"- **Discrete funding:** only at 8h settlements actually held through",
        f"- **Spike haircut:** {report['cost_assumptions']['spike_haircut']}",
        "",
        "### Cost assumptions (conservative)",
        "",
        f"| Item | Value |",
        f"|------|-------|",
        f"| Spot taker | {report['cost_assumptions']['spot_taker_bps']:.0f} bps |",
        f"| Perp taker | {report['cost_assumptions']['perp_taker_bps']:.0f} bps |",
        f"| Slippage / leg | {report['cost_assumptions']['slippage_bps_per_leg']:.0f} bps |",
        f"| Full round-trip | {report['cost_assumptions']['round_trip_cost_bps']:.0f} bps |",
        "",
        "## Summary",
        "",
    ]

    comm = report["portfolio_commercial"]
    ann_values = [r["net_annualized_pct"] for r in report["per_symbol_selective"]]
    ann_min = min(ann_values) if ann_values else 0.0
    ann_max = max(ann_values) if ann_values else 0.0

    lines.append(
        f"- Weighted net annualized (occupied capital, selective hold): "
        f"**{comm['weighted_net_annualized_pct']:.2f}%**"
    )
    lines.append(f"- Per-symbol range: **{ann_min:.2f}%** – **{ann_max:.2f}%**")
    lines.append(
        f"- Expected net USDT/year @ {COMMERCIAL_CAPITAL_USDT:.0f}U: "
        f"**{comm['expected_net_usdt_year']:.2f}**"
    )
    lines.append(
        f"- Qualifying symbols (positive edge): **{len(report['qualifying_symbols'])}** — "
        f"{', '.join(report['qualifying_symbols']) or 'none'}"
    )
    lines.append("")

    lines.extend(
        [
            "## Per-Symbol (selective hold)",
            "",
            "| Symbol | Held/Total | Cycles | Funding (bps) | Basis (bps) | Cost (bps) | Net (bps) | Net Ann % |",
            "|--------|------------|--------|---------------|-------------|------------|-----------|-----------|",
        ]
    )
    for row in sorted(
        report["per_symbol_selective"],
        key=lambda x: x["net_annualized_pct"],
        reverse=True,
    ):
        lines.append(
            f"| {row['symbol']} | {row['settlements_held']}/{row['settlements_total']} | "
            f"{row['position_cycles']} | {row['effective_funding_bps']:.1f} | "
            f"{row['basis_pnl_bps']:.1f} | {row['total_cost_bps']:.1f} | "
            f"{row['net_return_bps']:.1f} | {row['net_annualized_pct']:.2f} |"
        )
    lines.append("")

    lines.extend(
        [
            "## Per-Symbol (always hold — comparison)",
            "",
            "| Symbol | Funding (bps) | Basis (bps) | Cost (bps) | Net (bps) | Net Ann % |",
            "|--------|---------------|-------------|------------|-----------|-----------|",
        ]
    )
    for row in sorted(
        report["per_symbol_always_hold"],
        key=lambda x: x["net_annualized_pct"],
        reverse=True,
    ):
        lines.append(
            f"| {row['symbol']} | {row['effective_funding_bps']:.1f} | "
            f"{row['basis_pnl_bps']:.1f} | {row['total_cost_bps']:.1f} | "
            f"{row['net_return_bps']:.1f} | {row['net_annualized_pct']:.2f} |"
        )
    lines.append("")

    lines.extend(["## Edge Contribution", ""])
    for c in comm["edge_contributions"]:
        lines.append(
            f"- {c['symbol']}: {c['edge_share_pct']:.1f}% of positive edge "
            f"({c['net_annualized_pct']:.2f}% ann.)"
        )
    lines.append(f"- Top-2 concentration: **{comm['top2_contribution_pct']:.1f}%**")
    lines.append("")

    lines.extend(["## Capital Tiers (expected net USDT/year, selective hold)", ""])
    for cap, m in report["capital_tiers"].items():
        lines.append(
            f"- **{cap}U:** {m['expected_net_usdt_year']:.2f} USDT/yr "
            f"({m['weighted_net_annualized_pct']:.2f}% ann. on occupied capital)"
        )
    lines.append("")
    lines.append(
        "_Capacity assumption (v0): capital split equally across all loaded symbols; "
        "no depth/slippage scaling by size._"
    )
    lines.append("")

    lines.extend(["## Sensitivity (spot fee ±5bps, slippage)", ""])
    lines.append("| Δspot bps | slippage bps | Net Ann % | Net USDT/yr @4400U |")
    lines.append("|-----------|--------------|-----------|-------------------|")
    for row in report["sensitivity"]:
        lines.append(
            f"| {row['spot_fee_delta_bps']:+.0f} | {row['slippage_bps']:.0f} | "
            f"{row['weighted_net_annualized_pct']:.2f} | {row['expected_net_usdt_year']:.2f} |"
        )
    lines.append("")

    lines.extend(["## Three Regime Segments (30d each)", ""])
    for name, data in report["regimes"].items():
        status = "✅ stable" if data["stable"] else "❌ negative"
        lines.append(
            f"- **{name}**: ann={data['weighted_net_annualized_pct']:.2f}%, "
            f"net={data['expected_net_usdt_year']:.2f} USDT/yr, "
            f"qualifying={data['qualifying_count']} — {status}"
        )
    lines.append("")

    lines.extend(["## GO Checks (all required)", ""])
    for k, v in report["go_checks"].items():
        lines.append(f"- {'✅' if v else '❌'} `{k}`: {v}")
    lines.append("")

    if report["data_issues"]:
        lines.extend(["## Data Issues", ""])
        for issue in report["data_issues"]:
            lines.append(f"- {issue}")
        lines.append("")

    if report["verdict"] == "NO-GO":
        lines.extend(
            [
                "## Recommendation",
                "",
                "**Stop allocator / multi-executor build.** Archive as research tool.",
                "",
                "Mainstream coins' current-regime single-venue positive-funding cash-and-carry "
                "edge is too thin after conservative costs (fees, slippage, basis, turnover) "
                "to justify building a unified funding opportunity system.",
                "",
            ]
        )

    return "\n".join(lines)


def main() -> None:
    report = run_analysis()
    out_dir = Path(__file__).resolve().parent
    json_path = out_dir / "report.json"
    md_path = out_dir / "RESULT.md"

    with json_path.open("w") as f:
        json.dump(report, f, indent=2)

    md_path.write_text(render_markdown(report))

    comm = report["portfolio_commercial"]
    print(f"Verdict: {report['verdict']}")
    print(f"Weighted net ann (selective): {comm['weighted_net_annualized_pct']:.2f}%")
    print(f"Commercial net USDT/yr: {comm['expected_net_usdt_year']:.2f}")
    print(f"Qualifying: {len(report['qualifying_symbols'])}")
    print(f"Wrote {md_path}")


if __name__ == "__main__":
    main()
