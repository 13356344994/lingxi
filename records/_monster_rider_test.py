#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""候选⑤怪兽日骑手 首测(只读;预注册见 HANDOFF 记分牌 07-05)。

假设:币的杠杆结构开始瓦解会连环瓦解;打法=检测"怪兽态"后每个新暴动 episode 顺向进场 trail 出场。
与已证死"动量确认"的区别:赌 episode 频率的持续(被锁死杠杆仓连环出局),非单次运动延续。

预注册规则(写死在此,跑完不许改标准):
  怪兽态   = 滚动 30min 内 ≥5 次 census 暴动(|60s|≥25bps,census 自带 120s 同币去重);
             态终结 = 30min 无新暴动。
  瓦解型   = episode 时刻 OKX OI 较 30min 前 ≤ -1%(xmeta 5min 快照);否则平稳型;无OI数据=未知。
  episode  = 态内新暴动(同币距上个 episode ≥300s),方向=该暴动 ret60 符号;
             进场=事件后首个路径点(hot_mids,+1~+9s 容忍);
  出场 sim = trail:浮盈峰值≥+10bps 激活,自峰值回吐 20bps 即出;灾难线 -40bps;最长持有 300s;
             路径点稀疏(3s拍)按可得点近似。
  检测延迟对照 = 态"已知"时刻:1min 档(态成立+60s)vs 5min 档(+300s),episode 早于已知时刻不交易。
  过检锚 = episode 毛中位 ≥+15bps 且 ≥5 币有 episode 且 两日窗(07-04/07-05)分别为正 且 剔顶币仍正。
"""
import json
import bisect
import calendar
import collections
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
DAY_SPLIT = 1783209600  # 2026-07-05T00:00Z 两日窗分界(首跑曾误写成2025年值,分窗读数作废重跑)


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- census 暴动 ----
riots = collections.defaultdict(list)  # sym -> [(t, dir, abs60, fuel)]
for line in open(REC + "/fast_mover_census.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    t = r.get("recorded_at", "")
    if t < "2026-07-04T16:00":
        continue
    r60 = r.get("ret_60s_bps", 0) or 0
    riots[r["symbol"]].append((ep(t), 1.0 if r60 > 0 else -1.0, abs(r60),
                               float(r.get("market_liq_5m", 0) or 0)))
for v in riots.values():
    v.sort()

# ---- 价格路径(hot_mids 主标尺行 + backlog)----
paths = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            if not d.get("backlog_venue"):
                for ms, mid in d.get("backlog", []):
                    paths[d["backlog_sym"]].append((ms / 1000.0, mid))
            continue
        if d.get("recorded_at", "") < "2026-07-04T16:00":
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((T, mid))
for s in paths:
    paths[s] = sorted(set(paths[s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}

# ---- OKX OI 序列(xmeta 5min)----
oi = collections.defaultdict(list)
with open(REC + "/fast_xmeta_snapshots.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        T = ep(d["recorded_at"])
        for sym, v in (d.get("venues", {}).get("okx") or {}).items():
            if isinstance(v, list) and len(v) >= 2 and v[1]:
                oi[sym].append((T, float(v[1])))
for v in oi.values():
    v.sort()


def oi_at(sym, t, tol=600):
    arr = oi.get(sym)
    if not arr:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


def crumble(sym, t):
    a, b = oi_at(sym, t), oi_at(sym, t - 1800)
    if a is None or b is None or b <= 0:
        return None
    return (a - b) / b <= -0.01


def trail_sim(sym, t_in, direction):
    """入场后 trail 出场(规则见文件头)。返回毛收益 bps 或 None(路径不足)。"""
    arr = paths.get(sym)
    if not arr:
        return None
    keys = pk[sym]
    i = bisect.bisect_left(keys, t_in + 1.0)
    if i >= len(arr) or arr[i][0] - t_in > 9.0:
        return None
    entry = arr[i][1]
    peak = 0.0
    last = None
    for j in range(i + 1, len(arr)):
        T, m = arr[j]
        if T - t_in > 300:
            break
        ret = direction * (m - entry) / entry * 1e4
        last = ret
        peak = max(peak, ret)
        if ret <= -40:
            return ret
        if peak >= 10 and peak - ret >= 20:
            return ret
    return last  # max_hold 或路径尽头


# ---- 怪兽态区间 + episode 生成 ----
def run(detect_lag):
    out = []  # (ret, sym, day, crumble_flag)
    for sym, arr in riots.items():
        ts = [x[0] for x in arr]
        state_start = None
        known_at = None
        last_epi = 0.0
        for i, (t, d, a, fu) in enumerate(arr):
            lo = bisect.bisect_left(ts, t - 1800)
            cnt = i - lo + 1
            if state_start is None and cnt >= 5:
                state_start = t
                known_at = t + detect_lag
            if state_start is not None:
                if i > 0 and t - arr[i - 1][0] > 1800:  # 态已断,重置(本事件重新计数)
                    state_start = None
                    known_at = None
                    continue
                if known_at is not None and t >= known_at and t - last_epi >= 300:
                    ret = trail_sim(sym, t, d)
                    if ret is not None:
                        cr = crumble(sym, t)
                        out.append((ret, sym, "07-04" if t < DAY_SPLIT else "07-05", cr))
                        last_epi = t
    return out


def stat(rows, tag):
    if not rows:
        print("  %-26s n=0" % tag)
        return
    v = [r[0] for r in rows]
    syms = collections.Counter(r[1] for r in rows)
    top, topn = syms.most_common(1)[0]
    med = statistics.median(v)
    print("  %-26s n=%-4d 中位=%+7.1fbps 均值=%+7.1f 胜率=%3.0f%% 币数=%d top=%s:%d(%d%%)" % (
        tag, len(v), med, sum(v) / len(v),
        100 * sum(1 for x in v if x > 0) / len(v), len(syms), top, topn, 100 * topn // len(v)))
    return med


for lag, lt in ((60, "1min 档"), (300, "5min 档")):
    rows = run(lag)
    print("\n===== 检测延迟 %s =====" % lt)
    stat(rows, "全部 episode")
    stat([r for r in rows if r[3] is True], "瓦解型(OI 30min≤-1%)")
    stat([r for r in rows if r[3] is False], "平稳型(OI 未瓦解)")
    stat([r for r in rows if r[3] is None], "OI 未知")
    stat([r for r in rows if r[2] == "07-04"], "窗口 07-04")
    stat([r for r in rows if r[2] == "07-05"], "窗口 07-05")
    if rows:
        top = collections.Counter(r[1] for r in rows).most_common(1)[0][0]
        stat([r for r in rows if r[1] != top], "剔顶币")
print("\n过检锚(预注册):毛中位≥+15 且 ≥5币 且 两日窗均正 且 剔顶币仍正 → 提探针;瓦解vs平稳无差=对手模型错,判死。")
