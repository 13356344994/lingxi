#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E亏损杠杆研究(2026-07-08 用户纠偏"重心=怎样让结果变好";工程师口径,不记分)。

问题:格子口径的枪在亏——亏损从哪里漏?哪个杠杆能拧回来?每个杠杆给出:
  拧了之后的净中位/PF 变化 + 拦截率(付出多少供给) + 机制故事。
杠杆清单(全部入场时刻可知,零前视):
  L1 速度符号:gap_vel3(3s价差速度,正=已在收敛,负=还在扩大=接飞刀)
  L2 归因:anchor_ret15>0(锚在涨=价差因对面涨而开=追赶局) vs okx自跌(崩盘局)
  L3 弹射反事实:持仓中同币卖方burst时立即弹出(curve②标注,出场杠杆)
  L4 速度×归因 组合
数据=Go账本 rest 行(path_ok;可执行口径)。研究口径,产出候选闸走预注册。
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
    if r.get("module") not in (None, "", "e") or r.get("src") != "rest" or r.get("exit") == "unfilled" or not r.get("path_ok"):
        continue
    rows.append(r)
print("可执行×路径可知 行=%d" % len(rows))


def stat(sel, lab):
    if len(sel) < 10:
        print("  %-34s n=%d 不足" % (lab, len(sel)))
        return None
    v = [r["net_bps"] for r in sel]
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    med = statistics.median(v)
    print("  %-34s n=%-4d 净中位=%+7.1f PF=%.2f 占供给%3.0f%%" % (
        lab, len(sel), med, pw / pl if pl > 0 else 99, 100 * len(sel) / len(rows)))
    return med


base = stat(rows, "基线(全部rest×路径可知)")

print("\nL1 速度符号(3s价差速度;正=在收敛):")
conv = [r for r in rows if r.get("gap_vel3_bps", 0) > 0]
expd = [r for r in rows if r.get("gap_vel3_bps", 0) <= 0]
stat(conv, "已在收敛才进")
stat(expd, "还在扩大就进(接飞刀?)")

print("\nL2 归因(15s锚/自身收益;锚涨=追赶局,自跌=崩盘局):")
anchor_up = [r for r in rows if r.get("gap_anchor_ret15_bps", 0) > 0 and r.get("gap_okx_ret15_bps", 0) > -30]
okx_crash = [r for r in rows if r.get("gap_okx_ret15_bps", 0) <= -30]
stat(anchor_up, "锚上涨且自身没崩(追赶局)")
stat(okx_crash, "OKX自身15s跌穿30bps(崩盘局)")

print("\nL3 弹射反事实(curve②:持仓遇同币卖burst立即弹出):")
hit = [r for r in rows if r.get("burst_during_hold")]
if len(hit) >= 8:
    saved = [r.get("ret_at_burst_bps", 0) - r["gross_bps"] for r in hit]
    print("  命中burst行 n=%d | 实际毛中位=%+.1f | 弹射点毛中位=%+.1f | 每笔可省中位=%+.1f bps" % (
        len(hit), statistics.median([r["gross_bps"] for r in hit]),
        statistics.median([r.get("ret_at_burst_bps", 0) for r in hit]),
        statistics.median(saved)))
    all_net = [r["net_bps"] for r in rows]
    ej_net = [(r.get("ret_at_burst_bps", 0) - 12) if r.get("burst_during_hold") else r["net_bps"] for r in rows]
    print("  全体净中位:现行=%+.1f → 装弹射=%+.1f(反事实)" % (
        statistics.median(all_net), statistics.median(ej_net)))
else:
    print("  命中行=%d 不足" % len(hit))

print("\nL4 组合(收敛中×非崩盘局):")
combo = [r for r in rows if r.get("gap_vel3_bps", 0) > 0 and r.get("gap_okx_ret15_bps", 0) > -30]
stat(combo, "L1×L2")
combo2 = [r for r in rows if r.get("gap_vel3_bps", 0) > 0 and r.get("gap_okx_ret15_bps", 0) > -30 and r["bits"] & 1]
m = stat(combo2, "L1×L2×新鲜")
print("\n(研究口径;胜出杠杆→预注册前向,过门才进枪。)")
