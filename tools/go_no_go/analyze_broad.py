"""Broad-universe go/no-go: same rigorous selective-net model as analyze.py,
run across ALL data_broad/*.json (mid/small caps), to test the
'small coins are the real battlefield' hypothesis. Offline only."""

from __future__ import annotations

import glob
import json
from pathlib import Path

from config import CONSERVATIVE, OCCUPIED_CAPITAL_RATIO
from models import load_symbol_json, simulate_symbol

BROAD_DIR = Path(__file__).resolve().parent / "data_broad"


def vol_of(path: Path) -> float:
    try:
        d = json.load(path.open())
        return float(d.get("quote_volume_24h", 0.0)) / 1e6  # $M/day
    except Exception:
        return 0.0


def main() -> None:
    rows = []
    for fp in sorted(BROAD_DIR.glob("*.json")):
        if fp.name.startswith("_"):
            continue
        try:
            sym, settlements = load_symbol_json(fp)
        except Exception as exc:  # noqa: BLE001
            print(f"skip {fp.name}: {exc}")
            continue
        if len(settlements) < 10:
            continue
        r = simulate_symbol(sym, settlements, scenario=CONSERVATIVE, hold_rule="selective")
        rows.append((vol_of(fp), r))

    rows.sort(key=lambda x: x[1].net_annualized_pct, reverse=True)

    print(f"\n=== Broad selective-hold net (occupied capital, conservative costs) ===")
    print(f"{'symbol':<12}{'vol$M/d':>9}{'held/tot':>10}{'fundbps':>9}{'basisbps':>10}{'costbps':>9}{'netbps':>9}{'netAnn%':>9}")
    pos = 0
    for vol, r in rows:
        if r.net_annualized_pct > 0:
            pos += 1
        print(f"{r.symbol:<12}{vol:>9.1f}{r.settlements_held:>4}/{r.settlements_total:<5}"
              f"{r.effective_funding_bps:>9.1f}{r.basis_pnl_bps:>10.1f}{r.total_cost_bps:>9.1f}"
              f"{r.net_return_bps:>9.1f}{r.net_annualized_pct:>9.2f}")

    n = len(rows)
    print(f"\nTotal coins: {n}")
    print(f"Positive net edge: {pos}/{n} ({100*pos/n:.0f}%)")

    # Liquidity tiers
    for lo, hi, label in [(0, 5, "thin <$5M/d"), (5, 50, "mid $5-50M/d"), (50, 1e9, "liquid >$50M/d")]:
        tier = [r for vol, r in rows if lo <= vol < hi]
        if not tier:
            continue
        avg = sum(r.net_annualized_pct for r in tier) / len(tier)
        tpos = sum(1 for r in tier if r.net_annualized_pct > 0)
        best = max(tier, key=lambda r: r.net_annualized_pct)
        print(f"  {label:<16} n={len(tier):<3} avg netAnn={avg:>8.2f}%  positive={tpos}/{len(tier)}  best={best.symbol}({best.net_annualized_pct:.1f}%)")

    # Equal-weight portfolio over ALL coins, and over only-positive subset (cherry-picked ceiling)
    allr = [r for _, r in rows]
    ew = sum(r.net_annualized_pct for r in allr) / n
    posr = [r for r in allr if r.net_annualized_pct > 0]
    ewpos = (sum(r.net_annualized_pct for r in posr) / len(posr)) if posr else 0.0
    print(f"\nEqual-weight ALL coins net ann: {ew:.2f}%")
    print(f"Equal-weight ONLY-positive coins net ann (hindsight cherry-pick ceiling): {ewpos:.2f}%  (n={len(posr)})")
    print(f"Occupied-capital ratio: {OCCUPIED_CAPITAL_RATIO}x ; GO threshold: +45% net ann")


if __name__ == "__main__":
    main()
