#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""海况刹车信息集对齐审查(2026-07-06;三定理"分组条件必须过信息集审查"的执行)。

嫌疑:流水线 sea = 滚动前30笔确认事件 trail 结果(v-12)的中位,按 t0 顺序 append;
但 trail 窗口最长 120s——t0 相近的前几笔在决策当刻结果未知,离线回放提前用了它们。
对齐口径:sea_lag = 只用"窗口已完成"(t0_j + 120 <= t0_i)的最近30笔。
输出:①两口径决策翻转率 ②刹车价值(全流净合计/中位)两口径对比 ③判决窗(07-05 18Z后)
      通过刹车的事件数对比——若 lookahead 版价值蒸发,第二代的宪法部件带病,当场报。
"""
import os
import sys
import json
import statistics
import calendar
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
_loader = open(os.path.join(HERE, "_particle_pipeline.py")).read().split("if __name__ ==")[0]
exec(_loader)  # rows(已带 sea=lookahead 口径), ep, COST

WINDOW = 120.0  # trail cap
THRESH = -10.0
ROLL = 30

seq = sorted(rows, key=lambda e: e["t0"])

# lookahead 口径已在 loader 里算好(e["sea"]);这里重算 lag 口径
completed = []  # (t0, v-12),按 t0 序
for i, e in enumerate(seq):
    # 已完成=对方窗口在本事件 t0 前走完
    while_idx = 0
    # completed 单调,用指针法
    e["_i"] = i
ptr = 0
done = []  # (finish_time, net) 按 finish 时间排(t0 单调→finish 单调)
for e in seq:
    t = e["t0"]
    while ptr < len(seq) and seq[ptr]["t0"] + WINDOW <= t:
        done.append(seq[ptr]["v"] - COST)
        ptr += 1
    if len(done) >= ROLL:
        e["sea_lag"] = statistics.median(done[-ROLL:])
    else:
        e["sea_lag"] = 0.0  # 热身中性,与 lookahead 口径一致

FRESH = ep("2026-07-05T18:00:00")

def stats(tag, key):
    allowed = [e for e in seq if e[key] > THRESH]
    blocked = [e for e in seq if e[key] <= THRESH]
    nets = [e["v"] - COST for e in allowed]
    fresh_allow = [e for e in allowed if e["t0"] >= FRESH]
    print("%s: 放行 %d / 拦 %d | 放行净合计 %+8.1f 净中位 %+6.1f | 判决窗放行 %d" % (
        tag, len(allowed), len(blocked), sum(nets),
        statistics.median(nets) if nets else 0.0, len(fresh_allow)))
    return allowed

print("事件=%d | 刹车阈=%.0f 窗=%d trail完成=%.0fs\n" % (len(seq), THRESH, ROLL, WINDOW))
base = [e["v"] - COST for e in seq]
print("无刹车基线: n=%d 净合计 %+8.1f 净中位 %+6.1f" % (len(base), sum(base), statistics.median(base)))
a_look = stats("lookahead(现行)", "sea")
a_lag = stats("lag对齐(实盘可知)", "sea_lag")

flips = [e for e in seq if (e["sea"] > THRESH) != (e["sea_lag"] > THRESH)]
print("\n决策翻转: %d/%d (%.1f%%)" % (len(flips), len(seq), 100.0 * len(flips) / len(seq)))
f_open = [e for e in flips if e["sea_lag"] > THRESH]  # lag口径该放行、lookahead拦了
f_shut = [e for e in flips if e["sea_lag"] <= THRESH]
if f_open:
    nets = [e["v"] - COST for e in f_open]
    print("  lag放行/look拦下: n=%d 净合计 %+7.1f 中位 %+6.1f(刹车前视把这些'省'掉了——真实盘吃得到)" % (
        len(f_open), sum(nets), statistics.median(nets)))
if f_shut:
    nets = [e["v"] - COST for e in f_shut]
    print("  lag拦下/look放行: n=%d 净合计 %+7.1f 中位 %+6.1f" % (len(f_shut), sum(nets), statistics.median(nets)))

# 海况值差异分布
diffs = [abs(e["sea"] - e["sea_lag"]) for e in seq if len(seq) >= ROLL]
print("\n海况值 |look-lag| 分布: 中位 %.1f P90 %.1f 最大 %.1f" % (
    statistics.median(diffs), sorted(diffs)[int(len(diffs) * 0.9)], max(diffs)))
