#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""后验织网(㊵ v3;叠加态移植——全部格子同时持有后验,不过早坍缩;2026-07-08)。

每个(执行面×粒子组合)格子 = 一个假设,持有 P(净中位>0) 的滚动后验(bootstrap)。
镣铐(移植安全条例):①空区禁飞:n<15 不给后验;②挑选严厉度税:输出头部标注"从K格挑出";
③坍缩收费:本图零判决,后验下界穿门→走 Layer B 预注册,不直接上枪;④近期性:只用最近48h。
"""
import json
import random
import statistics
import calendar
import time
import itertools
import collections
from datetime import datetime

LEDGER = "/root/lingxi/records/okx_directional/particle_ledger.jsonl"
BITS = [("fresh", 1), ("calm", 2), ("breadth", 4), ("d4060", 8), ("d60", 16),
        ("nonstock", 32), ("habitual", 64), ("duty", 128), ("resid2040", 256)]
BITV = dict(BITS)
MIN_N = 15
BOOT = 400
random.seed(42)


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


now = time.time()
rows = []
for line in open(LEDGER):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    if r.get("exit") == "unfilled" or now - ep(r["time"]) > 48 * 3600:
        continue
    rows.append(r)
by_src = collections.defaultdict(list)
for r in rows:
    by_src[r.get("src", "det")].append(r)

# 格子字典:单粒子 + 含fresh的二元组 + 登记过的复合假设
combos = [((name,), bit) for name, bit in BITS]
for name, bit in BITS:
    if name != "fresh":
        combos.append((("fresh", name), 1 | bit))
combos += [
    (("fresh", "duty", "d4060"), 1 | 128 | 8),
    (("fresh", "calm", "d4060"), 1 | 2 | 8),
    (("fresh", "duty", "resid2040"), 1 | 128 | 256),
]


def posterior(nets):
    """bootstrap P(净中位>0)。"""
    n = len(nets)
    hits = 0
    for _ in range(BOOT):
        samp = [nets[random.randrange(n)] for _ in range(n)]
        if statistics.median(samp) > 0:
            hits += 1
    return hits / BOOT


web = []
examined = 0
for src in ("det", "rest", "mkr"):
    pool = by_src.get(src, [])
    for names, need in combos:
        nets = [r["net_bps"] for r in pool if r["bits"] & need == need]
        examined += 1
        if len(nets) < MIN_N:
            continue  # 空区禁飞
        p = posterior(nets)
        web.append((p, src, "×".join(names), len(nets), statistics.median(nets)))
web.sort(reverse=True)

print("后验织网(近48h;考察格=%d,有效格=%d;挑选严厉度税:头部是从 %d 格里挑的,后验≥0.97才够格谈预注册)" % (
    examined, len(web), examined))
hot = [w for w in web if w[0] >= 0.9]
print("亮区(P≥0.9)=%d 格 | 温区(0.7-0.9)=%d | 其余=%d" % (
    len(hot), len([w for w in web if 0.7 <= w[0] < 0.9]), len([w for w in web if w[0] < 0.7])))
print("%-6s %-5s %-28s %-5s %-8s %s" % ("P(赚)", "面", "格子", "n", "净中位", ""))
for p, src, name, n, med in web[:10]:
    print("%-6.2f %-5s %-28s %-5d %+8.1f %s" % (p, src, name, n, med, "←亮" if p >= 0.9 else ""))
print("最暗3格:")
for p, src, name, n, med in web[-3:]:
    print("%-6.2f %-5s %-28s %-5d %+8.1f" % (p, src, name, n, med))
print("(织网零判决;亮格走 Layer B 预注册坍缩,空区禁飞,n<%d 不显示。)" % MIN_N)
