#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E探针推荐书生成器(一军收束制:只答"明天开不开";2026-07-08)。

数据=三闸部署(07-08 04:34Z)后 xgap 影子里 gate_skip=""(全闸通过)的事件——
明天探针会打的完全同一批枪;出场用与实枪同规则模拟(账本路径)。
输出:①供给(枪/天,预算20笔要几天) ②模拟净值(中位/PF,成本12) ③漏斗(各闸拦截分布)
     ④最坏情况算术 ⑤开/不开的机器建议(用户拍板)。
"""
import json
import subprocess
import bisect
import calendar
import collections
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SEAL = "2026-07-08T04:34:00"  # 三闸部署时刻
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_seal = ep(SEAL)
out = subprocess.run(["grep", '"candidate_source":"xgap_rev"', REC + "/fast_executable_shadow_events.jsonl"],
                     stdout=subprocess.PIPE, universal_newlines=True).stdout
events, funnel = [], collections.Counter()
for line in out.splitlines():
    try:
        d = json.loads(line)
    except ValueError:
        continue
    if ep(d["recorded_at"]) < t_seal:
        continue
    skip = d.get("gate_skip") or "PASS"
    funnel[skip] += 1
    if skip == "PASS":
        events.append(d)

# 出场模拟:hot_mids 路径 + bybit 锚(与实枪同规则)
paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 45].split('"')[0]) < t_seal - 60:
                    continue
            except (ValueError, IndexError):
                pass
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            if d.get("backlog_venue") == "bybit":
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2:
                        anch[d["backlog_sym"]].append((pt[0] / 1000.0, pt[1]))
            elif not d.get("backlog_venue"):
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2:
                        paths[d["backlog_sym"]].append((pt[0] / 1000.0, pt[1]))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((T, mid))
        for sym, val in (d.get("anchors") or {}).get("bybit", {}).items():
            if isinstance(val, (list, tuple)) and len(val) >= 2:
                anch[sym].append((val[1] / 1000.0, val[0]))
for m in (paths, anch):
    for s in m:
        m[s] = sorted(set(m[s]))
ak = {s: [x[0] for x in arr] for s, arr in anch.items()}
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def a_at(sym, t):
    arr = anch.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(ak[sym], t) - 1
    if i < 0 or t - arr[i][0] > 8:
        return None
    return arr[i][1]


nets, spreads = [], []
for d in events:
    sym, t_in, entry = d["symbol"], ep(d["recorded_at"]), d.get("mid", 0)
    if entry <= 0 or sym not in paths:
        continue
    spreads.append(d.get("spread_bps", 0))
    arr = paths[sym]
    j = bisect.bisect_left(pk[sym], t_in)
    outv = None
    for k in range(j, len(arr)):
        T2, okx2 = arr[k]
        if T2 - t_in > 600:
            break
        by2 = a_at(sym, T2)
        ret = (okx2 - entry) / entry * 1e4
        outv = ret
        if ret <= -60:
            break
        if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
            break
    if outv is not None:
        nets.append(outv - COST)

hours = (max(ep(d["recorded_at"]) for d in events) - t_seal) / 3600 if events else 0
per_day = len(events) / max(hours, 0.1) * 24
print("=" * 64)
print("E 探针推荐书(数据=三闸部署后全闸通过的影子枪;%.1fh 窗)" % hours)
print("=" * 64)
print("① 供给:全闸通过 %d 枪(≈%.0f 枪/天)→ 20 笔探针预算约 %.1f 天打完" % (
    len(events), per_day, 20 / max(per_day, 0.1)))
print("   漏斗: %s" % dict(funnel.most_common()))
if nets:
    pw = sum(x for x in nets if x > 0)
    pl = -sum(x for x in nets if x < 0)
    pf = pw / pl if pl > 0 else 99
    med = statistics.median(nets)
    print("② 模拟净值(同规则出场,成本12): n=%d 净中位=%+.1f 净均=%+.1f PF=%.2f" % (
        len(nets), med, statistics.mean(nets), pf))
    print("   REST点差中位=%.1fbps(探针真实成本≈12+半点差)" % statistics.median(spreads))
    exp20 = statistics.mean(nets) * 20 / 1e4 * 5
    print("③ 算术:20笔×5U 期望≈%+.3fU | 最坏=缴械线 -0.5U | 单笔灾难上限≈-0.036U" % exp20)
    verdict = med > 0 and pf >= 1.0
    print("④ 机器建议:%s" % (
        "可开(影子口径为正;探针本职=买真滑点,期望为正时学费大概率<0.5U)" if verdict else
        "不建议开(全闸通过的枪影子仍为负——探针会按已知负期望送钱;建议再攒一天或等州变)"))
else:
    print("② 模拟净值:样本不足(%d),继续攒" % len(nets))
print("开/不开=用户拍板;agent 只递数字。")
