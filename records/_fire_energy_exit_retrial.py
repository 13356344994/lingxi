#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""火刀·能量刻度出场重审(2026-07-09;烟花翻案(57)的同病排查;只读)。

嫌疑:火刀账本出场=trail 激活10/回吐20/灾难-40/cap120s+二波止错——火刀点位(确认卖方 burst)
同样是暴动币,激活 10bps 在 75bps 噪声里=瞬间被噪声激活,回吐 20=瞬间被噪声洗出,
灾难 40=0.5 噪声步长。㊿ 判死火刀用的正是这把尺子。
重测参数与烟花翻案完全同款冻结(不另挑):unit=min(max(40,|入场r60|),200),
空单:灾难=1×unit / trail 0.8×激活 0.4×回吐 / cap600s;成本 12。
纪律 14b holdout=最后一天;12h 分片;单币主导报告;本脚本只提名,判决归 Go 账本。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

entries = []
with open(f"{REC}/particle_ledger.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("module") != "fire" or r.get("src") != "rest" or r.get("exit") == "unfilled":
            continue
        entries.append(dict(sym=r["symbol"], t0=ts(r["t0"]), old_net=r["net_bps"], old_exit=r["exit"]))
entries.sort(key=lambda e: e["t0"])
print(f"火刀账本行={len(entries)}")

idx = {}
for e in entries:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in entries}
lo_t, hi_t = entries[0]["t0"] - 70, entries[-1]["t0"] + 640
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        if t < lo_t or t > hi_t:
            continue
        row = None
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t + 70) - 1
            if j >= 0 and t0s[j] - 70 <= t <= t0s[j] + 630:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

rows = []
for e in entries:
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pre = [(t, m) for t, m in path if t < e["t0"]]
    pts = [(t, m) for t, m in path if t >= e["t0"]]
    if len(pts) < 3 or not pre:
        continue
    ent = pts[0][1]
    p60 = None
    for t, m in pre:
        if e["t0"] - t >= 55:
            p60 = m
    if p60 is None:
        if e["t0"] - pre[0][0] < 30:
            continue
        p60 = pre[0][1]
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(40.0, abs(r60)), 200.0)
    peak, px = 0.0, None
    for t, m in pts:
        u = -(m - ent) / ent * 1e4  # 空头口径
        if t - e["t0"] >= 600 or u <= -1.0 * unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    if not px:
        continue
    net = -(px - ent) / ent * 1e4 - COST
    rows.append(dict(e, net=net, unit=unit, r60=r60))
print(f"路径可评={len(rows)} 能量单位中位={statistics.median([r['unit'] for r in rows]):.0f}bps")

last = datetime.fromtimestamp(rows[-1]["t0"], tz=timezone.utc).date()

def rep(sub, tag, key="net"):
    if not sub:
        print(f"  {tag}: n=0")
        return
    n = [r[key] for r in sub]
    gp = sum(x for x in n if x > 0)
    gl = -sum(x for x in n if x < 0)
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    print(f"  {tag}: n={len(n):3d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

tr = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() < last]
ho = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() == last]
print("== 能量刻度空单 vs 旧尺子(同点位账本对照)==")
rep(tr, "train  能量")
rep(tr, "train  旧尺", "old_net")
rep(ho, f"holdout({last}) 能量")
rep(ho, f"holdout 旧尺", "old_net")

print("\n12h 分片(能量口径中位>0 ✓):")
t0min = rows[0]["t0"]
i = 0
while t0min + i * 43200 <= rows[-1]["t0"]:
    sl = [r["net"] for r in rows if t0min + i * 43200 <= r["t0"] < t0min + (i + 1) * 43200]
    if len(sl) >= 5:
        print(f"  片{i}: n={len(sl):3d} 中位{statistics.median(sl):+7.1f} {'✓' if statistics.median(sl)>0 else '✗'}")
    i += 1
