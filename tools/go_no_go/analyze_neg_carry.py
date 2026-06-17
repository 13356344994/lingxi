"""Quick GROSS negative-carry (perp-spot) ceiling from data_broad.
Negative funding -> earn by LONG perp + SHORT spot (needs borrow).
Captures -funding when prev settlement funding < 0. GROSS only:
NO borrow cost, NO fees, NO slippage, NO basis. This is an OPTIMISTIC ceiling."""

from __future__ import annotations

import glob
import json
from pathlib import Path

BROAD = Path(__file__).resolve().parent / "data_broad"
OCC = 1.6  # occupied capital ratio


def main() -> None:
    rows = []
    for fp in sorted(BROAD.glob("*.json")):
        if fp.name.startswith("_"):
            continue
        d = json.load(fp.open())
        sym = d["symbol"]
        fund = [(int(r["fundingTime"]), float(r["fundingRate"])) for r in d.get("funding", [])]
        fund.sort()
        if len(fund) < 10:
            continue
        days = max((fund[-1][0] - fund[0][0]) / 86_400_000.0, 1.0)
        # selective negative carry: hold iff previous funding < 0, capture -rate
        cap = 0.0
        held = 0
        for i in range(1, len(fund)):
            if fund[i - 1][1] < 0:
                cap += -fund[i][1]   # earn when funding negative
                held += 1
        gross_bps = cap * 10_000.0
        ann = (cap / OCC) * (365.0 / days) * 100.0
        vol = float(d.get("quote_volume_24h", 0)) / 1e6
        rows.append((sym, vol, held, len(fund), gross_bps, ann))

    rows.sort(key=lambda x: x[5], reverse=True)
    print(f"{'symbol':<12}{'vol$M':>8}{'held/tot':>10}{'grossBps':>10}{'grossAnn%':>10}")
    for sym, vol, held, tot, gb, ann in rows[:20]:
        print(f"{sym:<12}{vol:>8.1f}{held:>4}/{tot:<5}{gb:>10.1f}{ann:>10.2f}")
    n = len(rows)
    pos = sum(1 for r in rows if r[5] > 0)
    avg = sum(r[5] for r in rows) / n
    print(f"\nTotal: {n}  | gross-positive (before any cost): {pos}/{n}  | avg gross ann: {avg:.2f}%")
    print("NOTE: this is GROSS negative funding only. Real perp-spot net must also subtract:")
    print("  borrow interest (shorting the coin; often 10-50%+ APR on small caps),")
    print("  spot+perp fees, slippage, basis. GO threshold = +45% NET on occupied capital.")


if __name__ == "__main__":
    main()
