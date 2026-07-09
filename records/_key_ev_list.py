#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""四号任务交付:天气钥匙 EV 清单(2026-07-09;join 幸存组合的无条件期望直测;只读)。

join 产出(_prize_join_study):注意力钥匙=惯犯暴动币(prior24 AUC 0.91/0.91);
方向钥匙=①空头爆仓占优→向下(奖品条件下 71%/68% down)②卖burst占优→向上(55%/61% up)
③多头爆仓占优→向上(58%/51%)。本脚本做无条件 EV:信号出现即进场(不管有没有奖品),
这才是可交易期望。

冻结口径:触发=15min 窗内该侧爆仓 ≥2 笔且 > 对侧(爆仓钥匙)/该侧 burst≥3 且 > 对侧×1.5
(burst钥匙);惯犯闸=prior24h census 行 ≥134(join 冻结切点);同币 7200s 去重;剔主流;
进场=触发后首个 30s 网格点;出场=止损 -150bps 封顶(死板)+ 30m/120m 定点;成本 12。
报告按纪律 14i 天气分层(风暴/平静)+ train/holdout + 供给/容量。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
STOP = 150.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}
TRAIN = {"2026-07-06", "2026-07-07"}
HABIT = 134

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

grid, gridlast = {}, {}
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        if day_of(t) not in MINE:
            continue
        try:
            mids = json.loads(line).get("mids") or {}
        except Exception:
            continue
        for sym, m in mids.items():
            if m <= 0:
                continue
            if sym in gridlast and t - gridlast[sym] < 30:
                continue
            gridlast[sym] = t
            grid.setdefault(sym, []).append((t, m))

def load_sided(fn, key):
    out = {}
    with open(f"{REC}/{fn}") as f:
        for line in f:
            try:
                r = json.loads(line)
            except Exception:
                continue
            out.setdefault(r["symbol"], []).append((ts(r["recorded_at"]), r.get(key, "")))
    for v in out.values():
        v.sort()
    return out

liqs = load_sided("fast_liquidation_events.jsonl", "pos_side")
bursts = load_sided("fast_burst_detect.jsonl", "side")
for sym, arr in load_sided("fast_full_burst_probe.jsonl", "side").items():
    bursts.setdefault(sym, []).extend(arr)
for v in bursts.values():
    v.sort()
riots = load_sided("fast_mover_census.jsonl", "symbol")
liq_all_ts = sorted(t for arr in liqs.values() for t, _ in arr)

def cnt(idx, sym, lo, hi, want=None):
    a = idx.get(sym)
    if not a:
        return 0
    tsa = [x[0] for x in a]
    i, j = bisect.bisect_left(tsa, lo), bisect.bisect_right(tsa, hi)
    if want is None:
        return j - i
    return sum(1 for k in range(i, j) if a[k][1] == want)

def fuel_at(t):
    return bisect.bisect_right(liq_all_ts, t) - bisect.bisect_left(liq_all_ts, t - 300)

# ---- 触发生成 ----
def gen_liq_trigs(side):
    """side='short' → 空头爆占优做空钥匙;'long' → 多头爆占优做多钥匙。事件驱动:逐笔爆仓时刻判窗。"""
    out = []
    last = {}
    for sym, arr in liqs.items():
        if sym in MAJORS:
            continue
        for t, s in arr:
            if s != side:
                continue
            if day_of(t) not in MINE:
                continue
            if sym in last and t - last[sym] < 7200:
                continue
            n_side = cnt(liqs, sym, t - 900, t, side)
            n_opp = cnt(liqs, sym, t - 900, t, "long" if side == "short" else "short")
            if n_side < 2 or n_side <= n_opp:
                continue
            if cnt(riots, sym, t - 86400, t) < HABIT:
                continue
            last[sym] = t
            out.append(dict(sym=sym, t0=t))
    return out

def gen_burst_trigs(side):
    out = []
    last = {}
    for sym, arr in bursts.items():
        if sym in MAJORS:
            continue
        for t, s in arr:
            if s != side:
                continue
            if day_of(t) not in MINE:
                continue
            if sym in last and t - last[sym] < 7200:
                continue
            n_side = cnt(bursts, sym, t - 900, t, side)
            n_opp = cnt(bursts, sym, t - 900, t, "buy" if side == "sell" else "sell")
            if n_side < 3 or n_side <= 1.5 * n_opp:
                continue
            if cnt(riots, sym, t - 86400, t) < HABIT:
                continue
            last[sym] = t
            out.append(dict(sym=sym, t0=t))
    return out

CANDS = [
    ("轧空竭尽·空(short爆占优→短)", gen_liq_trigs("short"), -1),
    ("瀑布竭尽·多(long爆占优→多)", gen_liq_trigs("long"), 1),
    ("卖burst占优·多(逆卖压)", gen_burst_trigs("sell"), 1),
]

def simulate(trigs, sgn):
    rows = []
    for e in trigs:
        pts = grid.get(e["sym"])
        if not pts:
            continue
        tsa = [x[0] for x in pts]
        i0 = bisect.bisect_left(tsa, e["t0"])
        if i0 >= len(pts) or tsa[i0] - e["t0"] > 90:
            continue
        ent = pts[i0][1]
        seg = pts[i0:bisect.bisect_right(tsa, e["t0"] + 7500)]
        if len(seg) < 10:
            continue
        out = dict(e, fuel=fuel_at(e["t0"]), day=day_of(e["t0"]))
        for cap, tag in ((1800, "h30m"), (7200, "h2h")):
            px = None
            for t, m in seg:
                u = sgn * (m - ent) / ent * 1e4
                if t - e["t0"] >= cap or u <= -STOP:
                    px = m
                    break
                px = m
            out[tag] = max(sgn * (px - ent) / ent * 1e4, -STOP - 30) - COST  # 穿停以停+滑30计
        rows.append(out)
    return rows

print(f"{'候选':28s} {'集':8s} {'口径':5s} {'n':>5s} {'中位':>7s} {'均':>7s} {'PF':>5s} {'胜':>4s} 天气分层")
deliver = []
for name, trigs, sgn in CANDS:
    rows = simulate(trigs, sgn)
    for tag, dset in (("train", TRAIN), ("holdout", MINE - TRAIN)):
        sub = [r for r in rows if r["day"] in dset]
        for hz in ("h30m", "h2h"):
            n = [r[hz] for r in sub]
            if len(n) < 8:
                continue
            gp = sum(x for x in n if x > 0)
            gl = -sum(x for x in n if x < 0)
            storm = [r[hz] for r in sub if r["fuel"] >= 40]
            calm = [r[hz] for r in sub if r["fuel"] < 40]
            wx = ""
            if len(storm) >= 5:
                wx += f"风暴n{len(storm)}中位{statistics.median(storm):+.0f} "
            if len(calm) >= 5:
                wx += f"平静n{len(calm)}中位{statistics.median(calm):+.0f}"
            med = statistics.median(n)
            print(f"{name:28s} {tag:8s} {hz:5s} {len(n):5d} {med:+7.1f} {sum(n)/len(n):+7.1f} "
                  f"{(gp/gl if gl>0 else 99):5.2f} {sum(1 for x in n if x>0)/len(n)*100:3.0f}% {wx}")
            if tag == "holdout":
                deliver.append((med, name, hz, len(rows)))
    # 供给
    per_day = len(rows) / max(len(MINE), 1)
    print(f"{'':28s} 供给≈{per_day:.0f} 触发/日,币数={len(set(r['sym'] for r in rows))}")

print("\n== 交付清单(按 holdout 净中位排序)==")
for med, name, hz, n in sorted(deliver, reverse=True):
    print(f"  {med:+7.1f} bps/笔  {name} × {hz}(全窗 n={n})")
