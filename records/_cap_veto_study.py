#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""cap 否决位量化(2026-07-10 用户令四;保险栓不是新方向;只读,当场出分)。

论点:资费打 cap(|f|≥0.95%)=空头正被规则强制掏钱(每期付满 1%)=轧空高压区。
在这种币上"开空"(任何方向枪碰巧做空)=站在被规则压着的付租流对面=接轧空刀。EDGE 首枪死因。
量化:资费打 cap 期间"开空"的历史结局(3 个月资费史 × K 线),对比同期"非 cap 开空"基线。
若确认毒 → 通用否决位 `cap_short_veto`(此币此刻禁开空),record-only 先挂,实弹接线等批。

口径:钉死结算时刻(f≤-0.95%)= cap 态起点,持续到首个非钉死结算;
  "开空"进场点=钉死态内任一 1H bar 收盘,持有 4h/24h,方向=空,成本 12,止损 -150。
  对照=同币同期非 cap 态开空。train/holdout(最后14天)。
"""
import json, os, bisect, statistics
from datetime import datetime, timezone

H = "/root/lingxi/records/history"
COST = 12.0
STOP = 150.0
PIN = 0.0095

coins = {}
for fn in sorted(os.listdir(f"{H}/okx_1h")):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    rows = []
    with open(f"{H}/okx_1h/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
                rows.append((int(x[0]) / 1000.0, float(x[4])))
            except Exception:
                continue
    rows = sorted(set(rows))
    if rows:
        coins[sym] = rows
tsidx = {s: [x[0] for x in c] for s, c in coins.items()}

# 资费:标记每个结算是否钉死(负钉)
capstate = {}  # sym -> sorted [(settle_ts, pinned_bool)]
for fn in os.listdir(f"{H}/okx_funding"):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    if sym not in coins:
        continue
    rows = []
    with open(f"{H}/okx_funding/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
                fr = float(x.get("realizedRate") or x.get("fundingRate") or 0)
                ft = int(x.get("fundingTime", 0) or 0) / 1000.0
                if ft:
                    rows.append((ft, fr <= -PIN))
            except Exception:
                continue
    capstate[sym] = sorted(set(rows))

def in_cap(sym, t):
    """t 时刻该币是否处于负钉 cap 态(最近一次结算是钉死 且 距今<2 个结算间隔≈8h)。"""
    a = capstate.get(sym)
    if not a:
        return False
    ts = [x[0] for x in a]
    j = bisect.bisect_right(ts, t) - 1
    if j < 0 or t - a[j][0] > 8 * 3600:
        return False
    return a[j][1]

def short_ret(sym, t0, hz):
    c = coins.get(sym)
    tsa = tsidx.get(sym)
    if not tsa:
        return None
    i = bisect.bisect_right(tsa, t0) - 1
    j = bisect.bisect_right(tsa, t0 + hz) - 1
    if i < 0 or j <= i:
        return None
    p0 = c[i][1]
    if p0 <= 0:
        return None
    # 空头:逐 bar 找是否先穿 -STOP(空头亏=价涨)
    px = c[j][1]
    for k in range(i, j + 1):
        u = -(c[k][1] / p0 - 1) * 1e4
        if u <= -STOP:
            px = c[k][1]
            break
    return max(-(px / p0 - 1) * 1e4, -STOP - 30) - COST

# 采样:每币每 4h 一个开空点,标 cap/非cap
t_hi = max(c[-1][0] for c in coins.values())
split = t_hi - 14 * 86400
res = {"cap": {"tr": [], "ho": []}, "base": {"tr": [], "ho": []}}
res24 = {"cap": {"tr": [], "ho": []}, "base": {"tr": [], "ho": []}}
symct = {"cap_ho": {}}
for sym, c in coins.items():
    last = 0.0
    for t, _ in c:
        if t - last < 4 * 3600:
            continue
        last = t
        bucket = "cap" if in_cap(sym, t) else "base"
        part = "ho" if t >= split else "tr"
        r4 = short_ret(sym, t, 4 * 3600)
        r24 = short_ret(sym, t, 24 * 3600)
        if r4 is not None:
            res[bucket][part].append(r4)
        if r24 is not None:
            res24[bucket][part].append(r24)
        if bucket == "cap" and part == "ho" and r4 is not None:
            symct["cap_ho"][sym] = symct["cap_ho"].get(sym, 0) + 1

def rep(v, tag):
    if len(v) < 10:
        print(f"  {tag}: n={len(v)}(不足)")
        return
    gp = sum(x for x in v if x > 0); gl = -sum(x for x in v if x < 0)
    print(f"  {tag}: n={len(v):5d} 中位{statistics.median(v):+7.1f} 均{sum(v)/len(v):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in v if x>0)/len(v)*100:.0f}%")

print("=== 开空结局:cap 态 vs 非cap基线(空头视角;若cap态开空更毒=否决位成立)===")
print("[4h 持有]")
rep(res["cap"]["tr"], "cap态开空  [tr]")
rep(res["cap"]["ho"], "cap态开空  [ho]")
rep(res["base"]["tr"], "基线开空   [tr]")
rep(res["base"]["ho"], "基线开空   [ho]")
print("[24h 持有]")
rep(res24["cap"]["tr"], "cap态开空  [tr]")
rep(res24["cap"]["ho"], "cap态开空  [ho]")
rep(res24["base"]["tr"], "基线开空   [tr]")
rep(res24["base"]["ho"], "基线开空   [ho]")
print(f"cap态[ho]币分布 top:", sorted(symct['cap_ho'].items(), key=lambda kv:-kv[1])[:6])
print("\n判词:cap态开空中位 << 基线开空中位 且 train/holdout 同向 → 否决位成立(此币此刻禁开空)")
