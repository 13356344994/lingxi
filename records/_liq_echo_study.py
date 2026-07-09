#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""L1「回声」首测(2026-07-09;粒子方案㋁对手分层矿·L1 同行追猎者;只读)。

对手模型:官方爆仓推送是公共信号,全场追爆仓的 bot 同时收到(~0.5s 地板)。
推送后 0.5-2s 内出现成交爆量、且该窗口【无新增强平打印】= 非被迫流 = 追猎者群进场回声。
回声大 = 拥挤(我们这类玩家多)→ 顺流跟单的肉已被抢/反转更快;
回声小 = 池塘里只有我们 → 原始被迫流 edge 保留。
若成立:回声当【否决/让路粒子】(打不过同行就让,司天监读"今天这个猎场挤不挤")。

冻结口径(运行前登记):
  事件=episode 首爆(同币此前 600s 无爆仓);回声窗=(t+0.3s, t+2.5s],回声=窗内有 burst
  (双源:热币探测器+全市场探针)且窗内无新增强平;进场=t+1.5s 顺被迫流(多头爆→空),
  能量出场(unit=clip(|r60@t|,40,200),灾难1×/激活0.8×/回吐0.4×/cap600),成本12。
  覆盖率如实报(burst 仪器 07-06/07 只盖热币,07-08 起全市场;07-08=圣日不入分析)。
圣日协议:分析=07-06/07(train)+07-09(holdout);圣日不touch(本假设的圣日额度留给终验)。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
ANALYSIS = {"2026-07-06", "2026-07-07", "2026-07-09"}
HOLDOUT = "2026-07-09"

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# 爆仓流(episode 首爆)
liqs = {}
with open(f"{REC}/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        liqs.setdefault(r["symbol"], []).append((ts(r["recorded_at"]), r.get("pos_side", "")))
for s in liqs:
    liqs[s].sort()
events = []
for sym, arr in liqs.items():
    for i, (t, side) in enumerate(arr):
        if side not in ("long", "short"):
            continue
        if i > 0 and t - arr[i - 1][0] < 600:
            continue
        d = day_of(t)
        if d not in ANALYSIS:
            continue
        events.append(dict(sym=sym, t0=t, pos=side, day=d))
events.sort(key=lambda e: e["t0"])
print(f"episode 首爆(分析集三天)={len(events)}")

# burst 双源(带方向)
bursts = {}
for fn in ("fast_burst_detect.jsonl", "fast_full_burst_probe.jsonl"):
    with open(f"{REC}/{fn}") as f:
        for line in f:
            try:
                r = json.loads(line)
            except Exception:
                continue
            bursts.setdefault(r["symbol"], []).append(ts(r["recorded_at"]))
for s in bursts:
    bursts[s].sort()

def echo_of(e):
    """返回 (echo?, covered?):窗内 burst 且无新增强平;covered=该币该日 burst 仪器有任何数据。"""
    arr = bursts.get(e["sym"], [])
    covered = bool(arr) and any(abs(t - e["t0"]) < 43200 for t in (arr[bisect.bisect_left(arr, e["t0"]) - 1 if bisect.bisect_left(arr, e["t0"]) > 0 else 0], arr[min(bisect.bisect_left(arr, e["t0"]), len(arr) - 1)]))
    lo = bisect.bisect_right(arr, e["t0"] + 0.3)
    hi = bisect.bisect_right(arr, e["t0"] + 2.5)
    has_burst = hi > lo
    la = liqs.get(e["sym"], [])
    li = bisect.bisect_right([x[0] for x in la], e["t0"] + 0.3)
    lj = bisect.bisect_right([x[0] for x in la], e["t0"] + 2.5)
    more_liq = lj > li
    return (has_burst and not more_liq), covered, more_liq

for e in events:
    e["echo"], e["cov"], e["cascade"] = echo_of(e)
cov_by_day = {}
for e in events:
    cov_by_day.setdefault(e["day"], [0, 0])
    cov_by_day[e["day"]][1] += 1
    if e["cov"]:
        cov_by_day[e["day"]][0] += 1
print("burst 仪器覆盖率(该币±12h 有 burst 数据):",
      {d: f"{a}/{b}" for d, (a, b) in sorted(cov_by_day.items())})

# 路径
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
            j = bisect.bisect_right(t0s, t + 70) - 1  # +70:捕捉进场前窗(r60 原料)
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
for e in events:
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pre = [(t, m) for t, m in path if t < e["t0"]]
    pts = [(t - e["t0"], m) for t, m in path if t >= e["t0"] + 1.5]
    if len(pts) < 3 or not pre:
        continue
    p60 = None
    for t, m in pre:
        if e["t0"] - t >= 55:
            p60 = m
    if p60 is None:
        continue
    ent = pts[0][1]
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(abs(r60), 40.0), 200.0)
    sgn = -1 if e["pos"] == "long" else 1  # 顺被迫流:多头爆→空
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
print(f"路径可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 10:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    n = [r["net"] for r in sub]
    gp = sum(x for x in n if x > 0)
    gl = -sum(x for x in n if x < 0)
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    print(f"  {tag}: n={len(n):4d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

print("== 顺被迫流(能量出场)按回声分桶;cascade=窗内有新增强平(接力中,单列)==")
for dtag, cond in (("train(06/07)", lambda r: r["day"] != HOLDOUT), ("holdout(09)", lambda r: r["day"] == HOLDOUT)):
    sub = [r for r in rows if cond(r)]
    print(f"-- {dtag} --")
    rep([r for r in sub if r["echo"]], "回声(bot群到场)")
    rep([r for r in sub if not r["echo"] and not r["cascade"]], "无回声·无接力  ")
    rep([r for r in sub if r["cascade"]], "接力(新增强平)")
