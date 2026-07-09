#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""大额强平 tape 桶·扩样重审(2026-07-09;挖掘集清单最后一个 PF1.5 潜力候选;只读)。

旧读数:大额桶 trail 中位 +30.7/均 +52(n=11,不构成判决)。机制:大强平=真被迫流,
小强平=噪声;38ms burst 探测器领先官方推送 0.5-1.5s=私有测量。
冻结口径(运行前登记):
  事件=买/卖方 burst(双源合并,notional_1s≥2000=大额档,旧锚㋐同款)且官方爆仓在
  (burst, burst+3s] 内确认且方向对号(卖burst←多头爆/买burst←空头爆);
  进场=burst+1.5s 后首个标尺点,顺 burst 方向(taker);同币 600s 去重;剔主流币;
  出场=能量 trail(unit=clip(|r60|,40,200),灾难1×/激活0.8×/回吐0.4×/cap600);成本12。
  对照桶=小额(<2000)同规则。
圣日纪律:挖掘=06/07/09;PF≥1.3 才碰圣日(07-05+08),否则处决。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}
BIG = 2000.0

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# 官方爆仓(带方向)
liq = {}
with open(f"{REC}/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("pos_side") in ("long", "short"):
            liq.setdefault(r["symbol"], []).append((ts(r["recorded_at"]), r["pos_side"]))
for s in liq:
    liq[s].sort()

# burst 双源合并(1s 去重),确认+对号
entries = []
last = {}
seen = {}
for fn in ("fast_burst_detect.jsonl", "fast_full_burst_probe.jsonl"):
    with open(f"{REC}/{fn}") as f:
        for line in f:
            try:
                r = json.loads(line)
            except Exception:
                continue
            sym = r["symbol"]
            if sym in MAJORS:
                continue
            t0 = ts(r["recorded_at"])
            d = day_of(t0)
            if d not in MINE:
                continue
            k = (sym, int(t0))
            if k in seen:
                continue
            seen[k] = True
            entries.append(dict(sym=sym, t0=t0, side=r["side"], ntl=r.get("notional_1s", 0), day=d))
entries.sort(key=lambda e: e["t0"])
conf = []
for e in entries:
    arr = liq.get(e["sym"])
    if not arr:
        continue
    tsl = [x[0] for x in arr]
    lo = bisect.bisect_right(tsl, e["t0"])
    hi = bisect.bisect_right(tsl, e["t0"] + 3.0)
    if hi <= lo:
        continue
    want = "long" if e["side"] == "sell" else "short"
    match = sum(1 for i in range(lo, hi) if arr[i][1] == want)
    if match * 2 < hi - lo:
        continue  # 方向对号多数票(v6.2 同款)
    if e["sym"] in last and e["t0"] - last[e["sym"]] < 600:
        continue
    last[e["sym"]] = e["t0"]
    conf.append(e)
print(f"确认对号事件(挖掘集)={len(conf)} 其中大额≥{BIG:.0f}U={sum(1 for e in conf if e['ntl']>=BIG)}")

idx = {}
for e in conf:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in conf}
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        row = None
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t + 70) - 1
            if j >= 0 and t0s[j] - 70 <= t <= t0s[j] + 640:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

rows = []
for e in conf:
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pre = [(t, m) for t, m in path if t < e["t0"]]
    pts = [(t - e["t0"], m) for t, m in path if t >= e["t0"] + 1.5]
    if len(pts) < 3 or not pre:
        continue
    p60 = None
    for t, m in pre:
        if e["t0"] - t >= 55:
            p60 = m
    ent = pts[0][1]
    if p60 is None:
        continue
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(abs(r60), 40.0), 200.0)
    sgn = 1 if e["side"] == "buy" else -1  # 顺 burst 方向
    peak, px = 0.0, None
    for t, m in pts:
        u = sgn * (m - ent) / ent * 1e4
        if t >= 600 or u <= -unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px is None:
        continue
    rows.append(dict(e, net=sgn * (px - ent) / ent * 1e4 - COST))
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    n = [r["net"] for r in sub]
    gp = sum(x for x in n if x > 0)
    gl = -sum(x for x in n if x < 0)
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    print(f"  {tag}: n={len(n):3d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} 合计{sum(n):+7.0f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

rep([r for r in rows if r["ntl"] >= BIG], "大额桶(≥2kU)")
rep([r for r in rows if r["ntl"] < BIG], "小额对照     ")
print("大额桶按日:")
for d in sorted(MINE):
    rep([r for r in rows if r["ntl"] >= BIG and r["day"] == d], f"  {d}")
print("大额桶按方向:")
rep([r for r in rows if r["ntl"] >= BIG and r["side"] == "sell"], "  卖方burst(跟空)")
rep([r for r in rows if r["ntl"] >= BIG and r["side"] == "buy"], "  买方burst(跟多)")
