#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""跨币传染 lag-follow 首测(只读;候选④,HANDOFF 记分牌 07-05 预注册)。

数据现实约束(如实登记):全市场 mid 不落盘,非热币的连续路径不可得 → 本首测退化为
"事件级跟随率"检验,不是 bps 级收益检验:
  历史窗(前半)用暴动普查建 共暴亲和度(同分钟共暴次数/较少方总暴动分钟);
  测试窗(后半)对每个 A 暴动(|r60|≥40bps)取其头号伙伴 B(亲和≥0.25 且共暴≥5 分钟),
  若 B 前 3 分钟未暴 → 观察 B 是否在 180s 内以同向 ≥25bps 出现在普查里(=可吃级跟随)。
  对照 = B 在测试窗的无条件 3 分钟暴动率。共同因子检查 = 按 A 时刻燃料分桶。
可证伪锚(首测通过条件,通过≠过审,只决定是否值得做 bps 级精测):
  跟随率 ≥ 3× 无条件基线,且死水桶单独成立(排除"高燃料全场齐动"解释),n≥50。
"""
import json
import sys
import collections
import calendar
from datetime import datetime

DIR = "/root/lingxi/records/okx_directional"
HIST_END = "2026-07-05T02:00"   # 历史窗/测试窗切分(UTC)
MIN_CO = 5
MIN_AFF = 0.25
A_MIN_BPS = 40.0
FOLLOW_SEC = 180


def epoch(s):
    return calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())


hist_min = collections.defaultdict(set)   # minute -> set(sym)
sym_hist_min = collections.Counter()      # sym -> 暴动分钟数(历史窗)
test_events = []                          # (t_epoch, sym, dir, fuel)
test_min = collections.defaultdict(set)
sym_test_min = collections.Counter()
t_test_lo, t_test_hi = None, None

with open(DIR + "/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except ValueError:
            continue
        t = r.get("recorded_at", "")
        if t < "2026-07-04T16:00":
            continue
        sym = r["symbol"]
        mkey = t[:16]
        if t < HIST_END:
            if sym not in hist_min[mkey]:
                hist_min[mkey].add(sym)
                sym_hist_min[sym] += 1
        else:
            te = epoch(t)
            t_test_lo = te if t_test_lo is None else min(t_test_lo, te)
            t_test_hi = te if t_test_hi is None else max(t_test_hi, te)
            if sym not in test_min[mkey]:
                test_min[mkey].add(sym)
                sym_test_min[sym] += 1
            r60 = r.get("ret_60s_bps", 0) or 0
            test_events.append((te, sym, 1 if r60 > 0 else -1, abs(r60),
                                float(r.get("market_liq_5m", 0) or 0)))

# 共暴亲和度
co = collections.Counter()
for syms in hist_min.values():
    ss = sorted(syms)
    for i in range(len(ss)):
        for j in range(i + 1, len(ss)):
            co[(ss[i], ss[j])] += 1
partner = {}
for (a, b), c in co.items():
    if c < MIN_CO:
        continue
    aff = c / max(1, min(sym_hist_min[a], sym_hist_min[b]))
    if aff < MIN_AFF:
        continue
    for x, y in ((a, b), (b, a)):
        if x not in partner or partner[x][1] < aff:
            partner[x] = (y, aff)

print("历史窗共暴对(过滤后)=%d | 有头号伙伴的币=%d" % (
    sum(1 for c in co.values() if c >= MIN_CO), len(partner)))

# 测试窗:B 的分钟级暴动索引(带方向)
sym_events = collections.defaultdict(list)   # sym -> [(t, dir, absr60)]
for te, sym, d, a60, fu in test_events:
    sym_events[sym].append((te, d, a60))
for v in sym_events.values():
    v.sort()

import bisect


def b_rioted_between(sym, lo, hi, want_dir=None, min_bps=25.0):
    arr = sym_events.get(sym, [])
    i = bisect.bisect_left(arr, (lo, -9, -9.0))
    while i < len(arr) and arr[i][0] <= hi:
        if arr[i][2] >= min_bps and (want_dir is None or arr[i][1] == want_dir):
            return True
        i += 1
    return False


trials = collections.defaultdict(lambda: [0, 0])   # fuel_bucket -> [n, follow]
base_probe = collections.defaultdict(lambda: [0, 0])
for te, sym, d, a60, fu in test_events:
    if a60 < A_MIN_BPS or sym not in partner:
        continue
    B, aff = partner[sym]
    if b_rioted_between(B, te - 180, te, None, 25.0):
        continue  # B 已在动,无滞后可吃
    bucket = "死水(<20)" if fu < 20 else ("中间(20-40)" if fu < 40 else "活跃(>=40)")
    hit = b_rioted_between(B, te + 1, te + FOLLOW_SEC, d, 25.0)
    trials[bucket][0] += 1
    trials[bucket][1] += 1 if hit else 0

# 无条件基线:随机时刻 B 在 180s 内出现同向≥25bps 暴动的概率(用同一批 B、均匀采样时刻)
import random
random.seed(7)
bs = sorted({partner[s][0] for _, s, _, a, _ in test_events if a >= A_MIN_BPS and s in partner})
nb = 0
hb = 0
if t_test_lo and bs:
    for _ in range(4000):
        B = random.choice(bs)
        t0 = random.uniform(t_test_lo, max(t_test_lo + 1, t_test_hi - FOLLOW_SEC))
        if b_rioted_between(B, t0 - 180, t0, None, 25.0):
            continue
        nb += 1
        hb += 1 if b_rioted_between(B, t0 + 1, t0 + FOLLOW_SEC, random.choice((1, -1)), 25.0) else 0
base = hb / max(1, nb)
print("无条件基线(B 随机时刻 180s 同向暴动率)= %.2f%% (n=%d)" % (100 * base, nb))
print("\n== A暴动≥40bps 且 B(头号伙伴)未动 → B 180s 内同向≥25bps 跟随率 ==")
tot_n = tot_h = 0
for b in ("死水(<20)", "中间(20-40)", "活跃(>=40)"):
    n, h = trials[b]
    tot_n += n
    tot_h += h
    lift = (h / n / base) if n and base else 0
    print("  %-12s n=%-5d 跟随=%-4d 率=%5.1f%%  lift=%.1fx" % (b, n, h, 100 * h / max(1, n), lift))
print("  %-12s n=%-5d 跟随=%-4d 率=%5.1f%%  lift=%.1fx" % (
    "合计", tot_n, tot_h, 100 * tot_h / max(1, tot_n), (tot_h / tot_n / base) if tot_n and base else 0))
print("\n锚:合计 lift≥3x 且 死水桶单独成立 且 n≥50 → 值得做 bps 级精测(需先解决非热币路径数据)")
