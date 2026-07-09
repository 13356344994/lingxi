#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""方向钥匙·微结构半场(2026-07-09 夜班任务三①;只读)。

问题(用户唯一任务):惯犯币暴动后,往哪边打?
惯犯=滚动尺子(每个时点回看 24h 暴动数,全币种一视同仁,严禁今日名单套历史);
触发=census 暴动行 × 滚动惯犯 ≥134(join 冻结切点)× spread≤12 × 同币 3600s 去重;
方向信号(触发前 15min,全部走前):
  liq_net   = 空头爆-多头爆(正=轧空刚发生)
  burst_net = 买burst-卖burst(正=买方进攻)
  evap      = hot_micro 盘口蒸发(pull_bid-pull_ask,正=买侧撤空;坑内币才有,覆盖率如实报)
  gap       = 对 bybit 锚价差符号(hot_mids)
  本次暴动方向 r60 符号(价格信息,只作对照不单独成枪——价格自身规则已判全负)
每个信号测双向(顺/逆),30m 与 2h 持有,止损-150 死板,成本12;
出分=train(06/07)/holdout(09)×天气分层(风暴/平静/死水);
交付=按 holdout 净中位排序的候选清单+供给。死线制:当场出分。
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

# ---- 30s 网格(持有模拟)----
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
tsidx = {s: [x[0] for x in c] for s, c in grid.items()}

# ---- 信号流 ----
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
liq_all_ts = sorted(t for a in liqs.values() for t, _ in a)

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
    n = bisect.bisect_right(liq_all_ts, t) - bisect.bisect_left(liq_all_ts, t - 300)
    return "风暴" if n >= 40 else ("死水" if n < 20 else "平静")

# ---- 触发:census 暴动 × 滚动惯犯(riot 时刻回看 24h census 行数,走前)----
census = {}  # sym -> [(t, r60)]
with open(f"{REC}/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        census.setdefault(r["symbol"], []).append((ts(r["recorded_at"]), r.get("ret_60s_bps", 0.0), r.get("spread_bps", 99)))
for v in census.values():
    v.sort()

trigs = []
for sym, arr in census.items():
    if sym in MAJORS:
        continue
    tsa = [x[0] for x in arr]
    last = 0.0
    for i, (t, r60, sp) in enumerate(arr):
        if day_of(t) not in MINE or t - last < 3600:
            continue
        if sp <= 0 or sp > 12:
            continue
        prior24 = i - bisect.bisect_left(tsa, t - 86400)  # 滚动尺子:该时点回看24h
        if prior24 < HABIT:
            continue
        last = t
        trigs.append(dict(sym=sym, t0=t, r60=r60, day=day_of(t)))
trigs.sort(key=lambda e: e["t0"])
print(f"触发(惯犯滚动≥{HABIT}×暴动,3600s去重)={len(trigs)} 币数={len(set(e['sym'] for e in trigs))}")

# ---- 蒸发与锚(重扫描,针对性)----
need = {}
for e in trigs:
    need.setdefault(e["sym"], []).append(e["t0"])
for v in need.values():
    v.sort()
evap_at, gap_at = {}, {}
for fn in ("fast_hot_micro.jsonl.1", "fast_hot_micro.jsonl"):
    try:
        f = open(f"{REC}/{fn}")
    except FileNotFoundError:
        continue
    with f:
        for line in f:
            s1 = line.find('"symbol":"')
            p = line.find('"recorded_at":"')
            if p < 0 or s1 < 0:
                continue
            sym = line[s1 + 10:line.find('"', s1 + 10)]
            t0s = need.get(sym)
            if not t0s:
                continue
            try:
                t = ts(line[p + 15:p + 34])
            except Exception:
                continue
            j = bisect.bisect_left(t0s, t)
            if j >= len(t0s) or t0s[j] - t > 90 or t > t0s[j]:
                continue
            try:
                r = json.loads(line)
            except Exception:
                continue
            evap_at[(sym, t0s[j])] = (r.get("liquidity_pull_bid", 0) or 0) - (r.get("liquidity_pull_ask", 0) or 0)
with open(f"{REC}/fast_hot_mids.jsonl") as f:
    for line in f:
        if '"anchors"' not in line:
            continue
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        mids = d.get("mids") or {}
        for sym, anchor in ((d.get("anchors") or {}).get("bybit") or {}).items():
            t0s = need.get(sym)
            if not t0s:
                continue
            j = bisect.bisect_left(t0s, t)
            if j >= len(t0s) or t0s[j] - t > 90 or t > t0s[j]:
                continue
            m = mids.get(sym)
            if m and anchor[0] > 0:
                gap_at[(sym, t0s[j])] = (m - anchor[0]) / anchor[0] * 1e4
print(f"蒸发覆盖 {len(evap_at)}/{len(trigs)} 锚覆盖 {len(gap_at)}/{len(trigs)}")

# ---- 快照+持有模拟 ----
rows = []
for e in trigs:
    pts = grid.get(e["sym"])
    if not pts:
        continue
    tsa = tsidx[e["sym"]]
    i0 = bisect.bisect_left(tsa, e["t0"])
    if i0 >= len(pts) or tsa[i0] - e["t0"] > 90:
        continue
    ent = pts[i0][1]
    seg = pts[i0:bisect.bisect_right(tsa, e["t0"] + 7500)]
    if len(seg) < 10:
        continue
    t0 = e["t0"]
    sig = dict(
        liq_net=cnt(liqs, e["sym"], t0 - 900, t0, "short") - cnt(liqs, e["sym"], t0 - 900, t0, "long"),
        burst_net=cnt(bursts, e["sym"], t0 - 900, t0, "buy") - cnt(bursts, e["sym"], t0 - 900, t0, "sell"),
        evap=evap_at.get((e["sym"], t0)),
        gap=gap_at.get((e["sym"], t0)),
        r60=e["r60"],
    )
    # 双向持有(long/short)×(30m/2h),止损-150 死板
    res = {}
    for sgn, stag in ((1, "L"), (-1, "S")):
        for cap, htag in ((1800, "30m"), (7200, "2h")):
            px = None
            for t, m in seg:
                u = sgn * (m - ent) / ent * 1e4
                if t - t0 >= cap or u <= -STOP:
                    px = m
                    break
                px = m
            res[stag + htag] = max(sgn * (px - ent) / ent * 1e4, -STOP - 30) - COST
    rows.append(dict(e, sig=sig, res=res, wx=fuel_at(t0)))
print(f"可评={len(rows)}")

# ---- 方向规则测试(信号→方向;双持有;train/holdout×天气)----
RULES = [
    ("轧空后跟空(liq_net≥2→S)", lambda s: "S" if s["liq_net"] >= 2 else None),
    ("轧空后跟多(liq_net≥2→L)", lambda s: "L" if s["liq_net"] >= 2 else None),
    ("瀑布后跟多(liq_net≤-2→L)", lambda s: "L" if s["liq_net"] <= -2 else None),
    ("瀑布后跟空(liq_net≤-2→S)", lambda s: "S" if s["liq_net"] <= -2 else None),
    ("买burst占优→L(≥3)", lambda s: "L" if s["burst_net"] >= 3 else None),
    ("买burst占优→S(≥3)", lambda s: "S" if s["burst_net"] >= 3 else None),
    ("卖burst占优→L(≤-3)", lambda s: "L" if s["burst_net"] <= -3 else None),
    ("卖burst占优→S(≤-3)", lambda s: "S" if s["burst_net"] <= -3 else None),
    ("买侧蒸发→S(evap≥0.5)", lambda s: "S" if s["evap"] is not None and s["evap"] >= 0.5 else None),
    ("卖侧蒸发→L(evap≤-0.5)", lambda s: "L" if s["evap"] is not None and s["evap"] <= -0.5 else None),
    ("折价→L(gap≤-20)", lambda s: "L" if s["gap"] is not None and s["gap"] <= -20 else None),
    ("溢价→S(gap≥20)", lambda s: "S" if s["gap"] is not None and s["gap"] >= 20 else None),
    ("[对照]顺暴动(r60>0→L,<0→S)", lambda s: "L" if s["r60"] > 0 else "S"),
    ("[对照]逆暴动", lambda s: "S" if s["r60"] > 0 else "L"),
]

def stat(vals):
    if len(vals) < 8:
        return None
    gp = sum(x for x in vals if x > 0)
    gl = -sum(x for x in vals if x < 0)
    return dict(n=len(vals), med=statistics.median(vals), mean=sum(vals) / len(vals),
                pf=(gp / gl if gl > 0 else 99), win=sum(1 for x in vals if x > 0) / len(vals))

deliver = []
for name, rule in RULES:
    for htag in ("30m", "2h"):
        line = f"{name}×{htag}"
        parts = []
        ho_med = None
        for dtag, dset in (("tr", TRAIN), ("ho", MINE - TRAIN)):
            sel = []
            for r in rows:
                d = rule(r["sig"])
                if d is None or r["day"] not in dset:
                    continue
                sel.append((r["res"][d + htag], r["wx"]))
            st = stat([x[0] for x in sel])
            if st is None:
                parts.append(f"{dtag}:n<8")
                continue
            wxs = ""
            for wx in ("风暴", "平静", "死水"):
                v = [x[0] for x in sel if x[1] == wx]
                if len(v) >= 5:
                    wxs += f" {wx}{statistics.median(v):+.0f}"
            parts.append(f"{dtag}: n={st['n']} 中位{st['med']:+.1f} PF{st['pf']:.2f}{wxs}")
            if dtag == "ho":
                ho_med = st["med"]
        print(f"{line:32s} | {' | '.join(parts)}")
        if ho_med is not None:
            deliver.append((ho_med, line))

print("\n== 交付清单(holdout 净中位降序;供给=全窗触发) ==")
for med, line in sorted(deliver, reverse=True)[:10]:
    print(f"  {med:+7.1f} bps/笔  {line}")
