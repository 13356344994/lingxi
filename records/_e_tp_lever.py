#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E杠杆L5:提前落袋线(2026-07-08;工程师口径)。

漏点假设:出场只认"收敛≤10/600s/-60",不认浮盈——冲到过正数的单等不到收敛全吐回。
反事实:若 peak_bps≥T 即以 T 落袋(毛),否则维持实际出场;扫 T∈{5,10,15,20,30}。
注意:peak 采样=3s manage 节拍,与实际出场同源同分辨率(不占便宜);成本12不变。
产出:各 T 的净中位/PF/被触发占比 + "吐回税"体检(峰值为正却收负的单有多少肉被吐掉)。
"""
import json
import statistics

LEDGER = "/root/lingxi/records/okx_directional/particle_ledger.jsonl"
rows = []
for line in open(LEDGER):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    if r.get("src") != "rest" or r.get("exit") == "unfilled":
        continue
    rows.append(r)
print("rest行=%d" % len(rows))

# 吐回税体检
peaked_pos = [r for r in rows if r.get("peak_bps", 0) >= 5]
gaveback = [r for r in peaked_pos if r["net_bps"] < 0]
if peaked_pos:
    tax = [r["peak_bps"] - r["gross_bps"] for r in gaveback]
    print("吐回税:峰值≥+5bps 的单 n=%d,其中最终收负 n=%d(%.0f%%),吐回中位=%.1f bps" % (
        len(peaked_pos), len(gaveback), 100 * len(gaveback) / len(peaked_pos),
        statistics.median(tax) if tax else 0))

base = [r["net_bps"] for r in rows]
pw = sum(x for x in base if x > 0)
pl = -sum(x for x in base if x < 0)
print("基线: 净中位=%+.1f PF=%.2f" % (statistics.median(base), pw / pl if pl > 0 else 99))
print("%-8s %-8s %-8s %-8s %s" % ("落袋线T", "净中位", "PF", "触发率", "(peak≥T 即以 T-12 结算)"))
for T in (5, 10, 15, 20, 30):
    cf = []
    trig = 0
    for r in rows:
        if r.get("peak_bps", 0) >= T:
            cf.append(T - 12.0)
            trig += 1
        else:
            cf.append(r["net_bps"])
    pw = sum(x for x in cf if x > 0)
    pl = -sum(x for x in cf if x < 0)
    print("+%-7d %+-8.1f %-8.2f %3.0f%%" % (T, statistics.median(cf), pw / pl if pl > 0 else 99, 100 * trig / len(rows)))

# 组合:落袋线 × burst弹射(两杠杆叠加的反事实)
print("\n组合反事实:落袋线T × burst弹射(命中即弹) :")
for T in (15, 20, 30):
    cf = []
    for r in rows:
        if r.get("peak_bps", 0) >= T:
            cf.append(T - 12.0)
        elif r.get("burst_during_hold"):
            cf.append(r.get("ret_at_burst_bps", 0) - 12.0)
        else:
            cf.append(r["net_bps"])
    pw = sum(x for x in cf if x > 0)
    pl = -sum(x for x in cf if x < 0)
    print("  T=+%d × 弹射: 净中位=%+.1f PF=%.2f" % (T, statistics.median(cf), pw / pl if pl > 0 else 99))
