#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""耳朵价值研究:抢跑进场 vs 等确认进场(2026-07-06,用户质询"38ms优势没发挥到极致"的执行)。

现状:burst 38ms 探测 → 等官方确认(~0.5-1.5s)→ 进场,与纯听官方推送的人时序几乎一样,
耳朵只当了特征提纯器,没当抢跑器。
合法快车道假说(与"猎场几何=只见第二波"互证):**瀑布级确认代替单枪级确认**——
若探测当刻本币 30s 内已有官方爆仓(=瀑布已被官方背书),这枪 burst 不必再等自己的确认,
探测价直接进(实盘可知信息集:burst + 过去的官方推送,无前视)。
对照:A=探测价(t0,px_detect)进 trail;B=现枪(确认后 ~t0+1.4s 价)进 trail(=rows['v'])。
分桶:接力枪(t0 前 30s 已有官方爆仓)vs 首枪(没有,抢跑=赌确认)。
毛口径,不含滑点——给出的是耳朵价值上界。
"""
import os
import bisect
import statistics

HERE = os.path.dirname(os.path.abspath(__file__))
_loader = open(os.path.join(HERE, "_particle_pipeline.py")).read().split("if __name__ ==")[0]
exec(_loader)  # rows / paths / pk / liqd / ep / COST


def trail_from(sym, t0, entry_px, direction, cap=120.0):
    arr = paths.get(sym)
    keys = pk.get(sym)
    if not arr or not entry_px:
        return None
    i = bisect.bisect_left(keys, t0)
    peak, last = 0.0, None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - t0 > cap:
            break
        ret = direction * (m - entry_px) / entry_px * 1e4
        last = ret
        peak = max(peak, ret)
        if ret <= -40:
            return ret
        if peak >= 10 and peak - ret >= 20:
            return ret
    return last


def pstats(tag, vals):
    if not vals:
        print("%s: n=0" % tag)
        return
    wins = [v for v in vals if v > 0]
    print("%-34s n=%-3d 毛中位=%+7.1f 毛均=%+7.1f 顺向=%3.0f%%" % (
        tag, len(vals), statistics.median(vals), sum(vals) / len(vals),
        100.0 * len(wins) / len(vals)))


pairs = []  # (relay?, A, B)
for e in rows:
    if e.get("v") is None or not e.get("px"):
        continue
    a = trail_from(e["sym"], e["t0"], e["px"], e["dir"])
    if a is None:
        continue
    # 接力判定=探测当刻(t0)以前 30s 内已有官方爆仓(实盘可知,不含确认那笔)
    arr = liqd.get(e["sym"], [])
    keys = [t for t, _ in arr]
    lo = bisect.bisect_left(keys, e["t0"] - 30)
    hi = bisect.bisect_right(keys, e["t0"])
    relay = (hi - lo) >= 1
    pairs.append((relay, a, e["v"], e))

print("可对照事件=%d(毛口径,无滑点=上界)\n" % len(pairs))
print("== 全体确认事件 ==")
pstats("A 抢跑(探测价 t0+0s 进)", [p[1] for p in pairs])
pstats("B 现枪(确认后 ~t0+1.4s 进)", [p[2] for p in pairs])
print("\n== 接力枪(t0 前 30s 已有官方爆仓=瀑布已背书,抢跑合法)==")
rel = [p for p in pairs if p[0]]
pstats("A 抢跑", [p[1] for p in rel])
pstats("B 现枪", [p[2] for p in rel])
d = [p[1] - p[2] for p in rel]
if d:
    print("  每枪差值: 中位 %+.1f 均 %+.1f bps" % (statistics.median(d), sum(d) / len(d)))
print("\n== 首枪(t0 前无官方爆仓,抢跑=赌确认)==")
fst = [p for p in pairs if not p[0]]
pstats("A 抢跑", [p[1] for p in fst])
pstats("B 现枪", [p[2] for p in fst])

# 选择层叠加:接力×磁带活跃(base≥50)
rel_act = [p for p in rel if p[3].get("base", 0) >= 50]
print("\n== 接力枪 × 磁带活跃(base≥50,现役选择层粒子)==")
pstats("A 抢跑", [p[1] for p in rel_act])
pstats("B 现枪", [p[2] for p in rel_act])
