#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""M 程序(风暴州 90 分钟刀)·圣日协议重审(2026-07-09;窗内最后一个未审的分钟-小时级候选;只读)。

旧档案(㉚ 07-07):确认火 burst→做空,风暴跌潮州 n=29:15m 中位 -54.9(先被轧上去)→
90m +47.2/PF1.46(再阴跌回来);死因=①肉 47<预想 150 ②路径要扛 -55 回撤 ③n=29 in-sample;
复活条件=风暴州 n≥60 且 90m 中位≥100 或回撤收窄。今日窗口厚了 3 天,按圣日协议重审。

冻结口径(运行前登记,不扫描):
  事件=同币多头爆仓簇(30s 内 ≥2 笔 long 爆,episode=同币 90min 去重)且 t0 时刻风暴州
  (全市场 5min 爆仓 ≥40);进场=t0+3s 首个标尺点做空;剔主流币;成本 12;
  出场三口径并行:A=fix15m(旧读数对照,应为深负)/ B=fix90m(主口径,灾难=2×unit 兜底,
  unit=clip(|r60|,40,200)→灾难 80-400bps=扛得住轧空)/ C=fix90m 纯(无灾难,回答"路径回撤值不值扛")。
圣日纪律:挖掘=06/07/09;门=PF≥1.3 且分片同号才碰圣日,否则处决(14h)。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# 爆仓流
liq_all, liq_long = [], {}
with open(f"{REC}/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        t = ts(r["recorded_at"])
        liq_all.append(t)
        if r.get("pos_side") == "long":
            liq_long.setdefault(r["symbol"], []).append(t)
liq_all.sort()
for s in liq_long:
    liq_long[s].sort()

def fuel_at(t):
    return bisect.bisect_right(liq_all, t) - bisect.bisect_left(liq_all, t - 300)

# 事件:多头爆簇(30s≥2)×风暴州,90min episode 去重
events = []
for sym, arr in liq_long.items():
    if sym in MAJORS:
        continue
    lastT = 0.0
    for i, t in enumerate(arr):
        if t - lastT < 5400:
            continue
        lo = bisect.bisect_left(arr, t - 30)
        if i - lo + 1 < 2:
            continue
        d = day_of(t)
        if d not in MINE or t < ts("2026-07-05T07:10:00"):
            continue
        if fuel_at(t) < 40:
            continue
        events.append(dict(sym=sym, t0=t, day=d))
        lastT = t
events.sort(key=lambda e: e["t0"])
print(f"风暴州多头爆簇(挖掘集,90min去重)={len(events)} 币数={len(set(e['sym'] for e in events))}")
if not events:
    raise SystemExit(0)

idx = {}
for e in events:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in events}
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
            if j >= 0 and t0s[j] - 70 <= t <= t0s[j] + 5430:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

rows = []
for e in events:
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pre = [(t, m) for t, m in path if t < e["t0"]]
    pts = [(t - e["t0"], m) for t, m in path if t >= e["t0"] + 3]
    if len(pts) < 10 or not pre:
        continue
    ent = pts[0][1]
    p60 = None
    for t, m in pre:
        if e["t0"] - t >= 55:
            p60 = m
    if p60 is None:
        continue
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(abs(r60), 40.0), 200.0)
    out = dict(e, unit=unit)
    # A fix15m(带 2×unit 灾难)
    for cap, tag in ((900, "fix15m"), (5400, "fix90m")):
        px = None
        for t, m in pts:
            u = -(m - ent) / ent * 1e4
            if t >= cap or u <= -2.0 * unit:
                px = m
                break
            px = m
        if px:
            out[tag] = -(px - ent) / ent * 1e4 - COST
    # C fix90m 纯(无灾难)
    px = None
    for t, m in pts:
        if t >= 5400:
            px = m
            break
        px = m
    if px:
        out["pure90"] = -(px - ent) / ent * 1e4 - COST
    # 路径最深回撤(空头口径)
    mae = 0.0
    for t, m in pts:
        u = -(m - ent) / ent * 1e4
        if t < 5400:
            mae = min(mae, u)
    out["mae"] = mae
    rows.append(out)
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key in ("fix15m", "fix90m", "pure90"):
        n = [r[key] for r in sub if key in r]
        if not n:
            continue
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {key:6s}: n={len(n):3d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")
    maes = sorted(r["mae"] for r in sub)
    print(f"  {tag} 路径最深回撤: 中位{statistics.median(maes):+7.1f} P25 {maes[len(maes)//4]:+7.1f}(扛不扛得住的账)")

rep(rows, "全体")
print("按日:")
for d in sorted(MINE):
    rep([r for r in rows if r["day"] == d], f"  {d}")
