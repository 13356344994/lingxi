#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""暴动逆向·30m-2h 持有(2026-07-09;奖品地图倒推出的枪,终点视角首测;只读)。

倒推依据(_prize_map.py,n=52,820 奖品):分钟-小时尺度 85% 的肉有暴动前兆,
且大行情方向 64%(30m)/71%(2h) 逆前 15min 动量——肉是回吐形,尺度越长越确定。
对手模型:长尾拉砸=脉冲(操纵/FOMO/止损扫),无基本面流量续命,小时尺度回吐。

冻结口径(运行前登记):
  触发=census 暴动行(|r60|≥25 天然),长尾(剔主流+股票 perp 单列),spread≤12 可执行,
  同币 7200s 去重;方向=逆 15min 网格动量(不足则逆 r60);进场=触发后首个 30s 网格点;
  出场:A=fix30m 纯 / B=fix2h 纯 / C=fix2h+灾难 2×unit(unit=clip(|r60|,40,200))/
        D=fix2h+灾难 3×unit(宽停对照);成本 12。
  报账:中位/均值/PF/胜率/单币主导/按日分片/路径 MAE(扛单账)。
挖掘=06/07/09;圣日 05/08 不碰,PF≥1.3 且日同号才终验(14h:不过=处决)。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
STOCKS = {s + "-USDT" for s in ("SAMSUNG", "SKHYNIX", "SNDK", "AAPL", "SOXL", "TSLA", "NVDA", "MSTR", "COIN", "CRCL", "HOOD", "META", "AMZN", "GOOGL", "ORCL", "MCD")}
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# 30s 网格(挖掘日)
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

# 触发:census 暴动,7200s 去重
trigs = []
last = {}
with open(f"{REC}/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        sym = r["symbol"]
        if sym in MAJORS:
            continue
        sp = r.get("spread_bps", 99)
        if sp <= 0 or sp > 12:
            continue
        t0 = ts(r["recorded_at"])
        if day_of(t0) not in MINE:
            continue
        if sym in last and t0 - last[sym] < 7200:
            continue
        last[sym] = t0
        trigs.append(dict(sym=sym, t0=t0, r60=r.get("ret_60s_bps", 0.0), day=day_of(t0), stock=sym in STOCKS))
trigs.sort(key=lambda e: e["t0"])
print(f"触发(7200s去重)={len(trigs)} 币数={len(set(e['sym'] for e in trigs))}")

rows = []
for e in trigs:
    pts = grid.get(e["sym"])
    if not pts:
        continue
    tsa = [x[0] for x in pts]
    i0 = bisect.bisect_left(tsa, e["t0"])
    if i0 >= len(pts):
        continue
    ent_t, ent = pts[i0]
    if ent_t - e["t0"] > 90:
        continue
    j = bisect.bisect_right(tsa, e["t0"] - 900) - 1
    mom = 0.0
    if 0 <= j < i0 and pts[j][1] > 0:
        mom = (ent - pts[j][1]) / pts[j][1] * 1e4
    if mom == 0:
        mom = e["r60"]
    if mom == 0:
        continue
    sgn = -1 if mom > 0 else 1  # 逆动量
    unit = min(max(abs(e["r60"]), 40.0), 200.0)
    seg = pts[i0:bisect.bisect_right(tsa, e["t0"] + 7500)]
    if len(seg) < 10:
        continue
    out = dict(e, mom=mom)
    # A/B 纯定点
    for cap, tag in ((1800, "fix30m"), (7200, "fix2h")):
        px = None
        for t, m in seg:
            if t - e["t0"] >= cap:
                px = m
                break
            px = m
        out[tag] = sgn * (px - ent) / ent * 1e4 - COST
    # C/D 带灾难
    for k, tag in ((2.0, "dis2u"), (3.0, "dis3u")):
        px = None
        for t, m in seg:
            u = sgn * (m - ent) / ent * 1e4
            if t - e["t0"] >= 7200 or u <= -k * unit:
                px = m
                break
            px = m
        out[tag] = sgn * (px - ent) / ent * 1e4 - COST
    mae = 0.0
    for t, m in seg:
        if t - e["t0"] < 7200:
            mae = min(mae, sgn * (m - ent) / ent * 1e4)
    out["mae"] = mae
    rows.append(out)
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key in ("fix30m", "fix2h", "dis2u", "dis3u"):
        n = [r[key] for r in sub if key in r]
        if not n:
            continue
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {key:6s}: n={len(n):4d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")
    maes = sorted(r["mae"] for r in sub)
    print(f"  {tag} 2h路径MAE: 中位{statistics.median(maes):+7.1f} P25 {maes[len(maes)//4]:+7.1f}")

crypto = [r for r in rows if not r["stock"]]
rep(crypto, "加密长尾")
rep([r for r in rows if r["stock"]], "股票perp")
print("加密长尾按日(fix2h):")
for d in sorted(MINE):
    sub = [r["fix2h"] for r in crypto if r["day"] == d]
    if len(sub) >= 8:
        print(f"  {d}: n={len(sub):4d} 中位{statistics.median(sub):+7.1f} {'✓' if statistics.median(sub)>0 else '✗'}")
print("按暴动烈度(加密长尾,fix2h;肉该随烈度涨=机制自证):")
for lo, hi, tag in ((25, 40, "25-40"), (40, 80, "40-80"), (80, 9999, "≥80")):
    sub = [r["fix2h"] for r in crypto if lo <= abs(r["r60"]) < hi]
    if len(sub) >= 8:
        gp = sum(x for x in sub if x > 0)
        gl = -sum(x for x in sub if x < 0)
        print(f"  |r60|∈[{tag:6s}): n={len(sub):4d} 中位{statistics.median(sub):+7.1f} PF{(gp/gl if gl>0 else 99):.2f}")
