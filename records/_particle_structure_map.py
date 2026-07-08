#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""粒子承重结构图(迷宫操作循环·地图层;2026-07-08 用户"迷宫/来回穿梭/思考组合"纠偏的执行)。

与"挑最好组合"(㊃死法)相反:本图只回答结构问题——
  ①边际承重:每个粒子 on vs off 的净值差(跨全部组合平均=它普遍承重还是孤峰)
  ②交互结构:粒子对的协同(1+1>2)与相消(1+1<2)
  ③执行面差异:同一粒子在 det/rest/mkr 三口径下承重是否翻向(执行方式本身是粒子)
产出=重组层的思考原料,不产生任何判决;判决仍走预注册。
"""
import json
import itertools
import statistics
import sys
import collections

LEDGER = "/root/lingxi/records/okx_directional/particle_ledger.jsonl"
BITS = [("fresh", 1), ("calm", 2), ("breadth", 4), ("d4060", 8), ("d60", 16),
        ("nonstock", 32), ("habitual", 64), ("duty", 128), ("resid2040", 256)]


def med(v):
    return statistics.median(v) if v else 0.0


def pf(v):
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    return pw / pl if pl > 0 else (99 if pw > 0 else 0)


rows = []
for line in open(LEDGER):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    if r.get("exit") == "unfilled":
        continue
    rows.append(r)
by_src = collections.defaultdict(list)
for r in rows:
    by_src[r.get("src", "det")].append(r)
print("账本行: " + " ".join("%s=%d" % (s, len(v)) for s, v in sorted(by_src.items())))

print("\n== ① 边际承重(粒子 on−off 的净中位差;|差|大且跨口径同向=承重梁) ==")
print("%-10s" % "粒子" + "".join("%16s" % s for s in ("det", "rest", "mkr")))
marg = {}
for name, bit in BITS:
    line = "%-10s" % name
    for src in ("det", "rest", "mkr"):
        v = by_src.get(src, [])
        on = [r["net_bps"] for r in v if r["bits"] & bit]
        off = [r["net_bps"] for r in v if not r["bits"] & bit]
        if len(on) >= 15 and len(off) >= 15:
            d = med(on) - med(off)
            marg[(name, src)] = d
            line += "%+9.1f(n%3d)" % (d, len(on))
        else:
            line += "%16s" % ("n=%d 不足" % len(on))
    print(line)

print("\n== ② 交互结构(粒子对:合体中位 − 两单件中位之和;正=协同 负=相消;n≥20) ==")
inter = []
for src in ("det", "rest"):
    v = by_src.get(src, [])
    if len(v) < 60:
        continue
    for (n1, b1), (n2, b2) in itertools.combinations(BITS, 2):
        both = [r["net_bps"] for r in v if r["bits"] & b1 and r["bits"] & b2]
        only1 = [r["net_bps"] for r in v if r["bits"] & b1 and not r["bits"] & b2]
        only2 = [r["net_bps"] for r in v if r["bits"] & b2 and not r["bits"] & b1]
        neither = [r["net_bps"] for r in v if not r["bits"] & b1 and not r["bits"] & b2]
        if min(len(both), len(only1), len(only2), len(neither)) < 20:
            continue
        base = med(neither)
        synergy = (med(both) - base) - ((med(only1) - base) + (med(only2) - base))
        inter.append((abs(synergy), synergy, n1, n2, src, len(both)))
inter.sort(reverse=True)
for _, syn, n1, n2, src, n in inter[:8]:
    kind = "协同" if syn > 0 else "相消"
    print("  %-9s × %-9s [%s] %s %+7.1f bps (合体n=%d)" % (n1, n2, src, kind, syn, n))

print("\n== ③ 执行面翻向(同一粒子 det/rest/mkr 承重符号不一致=执行方式是它的隐藏搭档/天敌) ==")
for name, _ in BITS:
    vals = [(s, marg.get((name, s))) for s in ("det", "rest", "mkr")]
    known = [(s, d) for s, d in vals if d is not None]
    if len(known) >= 2 and (max(d for _, d in known) > 2 and min(d for _, d in known) < -2):
        print("  %-10s " % name + "  ".join("%s=%+.1f" % (s, d) for s, d in known) + "  ← 翻向")

print("\n(地图不判决;重组层从承重梁与协同对里出复合假设,走预注册。)")
