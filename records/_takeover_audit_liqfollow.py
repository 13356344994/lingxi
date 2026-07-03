#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""接手审计(只读,不影响交易):独立复核 liq_follow 线。

① 离线镜像复现:用爆仓流自建簇(30s内同向净≥2笔),base shadow 做价格标尺,
   施加实盘同款闸(有历史/imp15同向≥5/点差≤12/深度5k-15万),分向分时段看前向收益——
   检验"+7.8bps@60s 跟空"在实盘信息集下是否成立、是否随时段衰减;
② 覆盖率:多少可交易簇发生在无标尺覆盖的币上(离线依据外推缺口);
③ 实盘单全生命周期:事件→成交延迟、入场价 vs 事件mid(真实进场成本)、出场原因、
   出场后价格反事实(标尺可covered的用shadow;含"若持有到60/180s"毛收益);
④ 费用结构:每笔实扣费 vs 毛利。
"""
import bisect
import collections
import datetime
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
LIQ = os.path.join(DIR, "fast_liquidation_events.jsonl")
SHADOW = os.path.join(DIR, "fast_executable_shadow_events.jsonl")
SETTLE = os.path.join(DIR, "fast_v2_settlements.jsonl")


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


# ---------- 载入爆仓流 ----------
liqs = []  # (t, symbol, pos_side)
with open(LIQ) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        liqs.append((ts(d["recorded_at"]), d["symbol"], d.get("pos_side", "")))
liqs.sort(key=lambda x: x[0])
print(f"爆仓流: {len(liqs)} 条  {liqs[0][0]} → {liqs[-1][0]}")

# ---------- 流式载入 shadow ----------
base = collections.defaultdict(list)   # sym -> [(t, mid)]  仅 base(candidate_source 空)
base_ev = collections.defaultdict(list)  # sym -> [(t, spread, depth10)]
lf_events = {}  # snapshot_id -> dict(liq_follow 候选事件)
t0 = None
with open(SHADOW) as f:
    for line in f:
        # 快速过滤:只解析今天的
        i = line.find('"recorded_at":"')
        if i < 0:
            continue
        rt = line[i + 15:i + 34]
        if rt < "2026-07-03T04:00:00":  # 爆仓采集部署(04:12 UTC)之前的不需要
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        t = ts(d["recorded_at"])
        src = d.get("candidate_source", "")
        sym = d["symbol"]
        if src == "":
            base[sym].append((t, d["mid"]))
            base_ev[sym].append((t, d.get("spread_bps", 0), d.get("depth_notional_10bps", 0)))
        elif src == "liq_follow":
            lf_events[d.get("snapshot_id", "")] = d
            # liq_follow 评估事件的 mid 也可作稀疏标尺(非 base 币唯一价格来源)
            base[sym].append((t, d["mid"]))
for s in base:
    base[s].sort()
    base_ev[s].sort()
print(f"标尺覆盖币数: {len(base)}  liq_follow 候选事件: {len(lf_events)}")


def mid_at(sym, t, tol=45):
    """<=t 的最近 mid(tol 秒内)。"""
    arr = base.get(sym)
    if not arr:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or (t - arr[i][0]).total_seconds() > tol:
        return None
    return arr[i][1]


def fwd_ret(sym, t, mid0, secs, follow):
    """t+secs 的 mid(之后 30s 容差),按 follow 方向的 bps 收益。"""
    arr = base.get(sym)
    if not arr or mid0 is None or mid0 <= 0:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(arr):
        return None
    tt, m = arr[i]
    if (tt - t).total_seconds() > secs + 30:
        return None
    r = (m - mid0) / mid0 * 10000
    return -r if follow == "short" else r


def imp_bps(sym, t, secs):
    """t 时刻回看 secs 的冲量(标尺口径,近似实盘 imp15)。"""
    m_now = mid_at(sym, t)
    arr = base.get(sym)
    if m_now is None or not arr:
        return None
    target = t - datetime.timedelta(seconds=secs)
    keys = [x[0] for x in arr]
    i = bisect.bisect_right(keys, target) - 1
    if i < 0 or (target - arr[i][0]).total_seconds() > 30:
        return None
    past = arr[i][1]
    if past <= 0:
        return None
    return (m_now - past) / past * 10000


# ---------- ① 自建簇 + 实盘同款闸 ----------
# 簇判定:该币 30s 滑窗内 long/short 爆仓计数,净向≥2;15s 评估冷却去重(与实盘一致)
per_sym = collections.defaultdict(list)
for t, sym, ps in liqs:
    per_sym[sym].append((t, ps))

clusters = []  # (t, sym, follow_side)
for sym, arr in per_sym.items():
    last_eval = None
    for idx, (t, ps) in enumerate(arr):
        if last_eval is not None and (t - last_eval).total_seconds() < 15:
            continue
        nl = ns = 0
        for tt, pps in arr[max(0, idx - 200):idx + 1]:
            if (t - tt).total_seconds() <= 30 and (t - tt).total_seconds() >= 0:
                if pps == "long":
                    nl += 1
                elif pps == "short":
                    ns += 1
        if nl + ns < 2 or nl == ns:
            continue
        follow = "short" if nl > ns else "long"
        clusters.append((t, sym, follow))
        last_eval = t
clusters.sort(key=lambda x: x[0])
print(f"\n自建簇(净向≥2,15s去重): {len(clusters)}")

WINDOWS = (15, 30, 60, 180)
covered = uncovered = 0
gated = []  # 通过全部实盘闸的簇
raw = []    # 只有标尺、未加闸
for t, sym, follow in clusters:
    m0 = mid_at(sym, t)
    if m0 is None:
        uncovered += 1
        continue
    covered += 1
    raw.append((t, sym, follow, m0))
    # 实盘闸近似:imp15 同向且 ≥5bps
    i15 = imp_bps(sym, t, 15)
    if i15 is None:
        continue
    if follow == "short" and i15 > -5:
        continue
    if follow == "long" and i15 < 5:
        continue
    # 点差/深度闸(就近 base 事件)
    bev = base_ev.get(sym)
    ok_gate = True
    if bev:
        keys = [x[0] for x in bev]
        j = bisect.bisect_right(keys, t) - 1
        if j >= 0 and (t - bev[j][0]).total_seconds() <= 60:
            _, sp, dp = bev[j]
            if sp > 12 or dp < 5000 or dp > 150000:
                ok_gate = False
    if not ok_gate:
        continue
    gated.append((t, sym, follow, m0))

print(f"标尺覆盖簇: {covered}  无覆盖(外推缺口): {uncovered}  ({uncovered/(covered+uncovered)*100:.0f}% 的簇发生在离线依据看不见的币上)")


def report(tag, items, side=None):
    sub = [x for x in items if side is None or x[2] == side]
    if not sub:
        print(f"  {tag}: n=0")
        return
    print(f"  {tag} (n={len(sub)}):")
    for w in WINDOWS:
        rs = [fwd_ret(s, t, m0, w, f) for t, s, f, m0 in sub]
        rs = [r for r in rs if r is not None]
        if not rs:
            continue
        pos = sum(1 for r in rs if r > 0)
        print(f"    +{w:3d}s n={len(rs):4d} 顺向率={pos/len(rs)*100:4.1f}% 均值={sum(rs)/len(rs):+6.2f}bps 中位={sorted(rs)[len(rs)//2]:+6.2f}")


print("\n== 全日·未加闸(对照组) ==")
report("跟空(多头爆)", raw, "short")
report("跟多(空头爆)", raw, "long")
print("\n== 全日·实盘同款闸后(imp15同向≥5 + 点差/深度) ==")
report("跟空", gated, "short")
report("跟多", gated, "long")

print("\n== 跟空·加闸·分 3h 时段(稳定性) ==")
if gated:
    lo = min(x[0] for x in gated)
    for k in range(8):
        w0 = lo + datetime.timedelta(hours=3 * k)
        w1 = w0 + datetime.timedelta(hours=3)
        sub = [x for x in gated if w0 <= x[0] < w1 and x[2] == "short"]
        if sub:
            report(f"{w0.strftime('%H:%M')}–{w1.strftime('%H:%M')}", sub)

# ---------- ③ 实盘单全生命周期 ----------
print("\n== ③ v4换枪后 liq_follow 实盘单解剖 ==")
recs = []
with open(SETTLE) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("fast_candidate_source") == "liq_follow" and d["time"] >= "2026-07-03T12:32":
            recs.append(d)
tot_fee = tot_real = 0.0
rows = []
for r in recs:
    t_close = ts(r["time"])
    hold = r.get("hold_seconds", 0)
    t_open = t_close - datetime.timedelta(seconds=hold)
    ev = lf_events.get(r.get("fast_event_id", ""))
    ev_mid = ev["mid"] if ev else None
    delay = None
    if r.get("source_event_time"):
        delay = (t_open - ts(r["source_event_time"])).total_seconds()
    # 入场成本:成交价 vs 事件mid(short卖出低于mid=付出)
    cost = None
    if ev_mid:
        cost = (ev_mid - r["entry"]) / ev_mid * 10000
        if r["side"] == "long":
            cost = -cost
    # 反事实:从入场 mid 持有 60/180s 的毛收益(标尺)
    cf60 = fwd_ret(r["symbol"], t_open, r["entry"], 60, r["side"])
    cf180 = fwd_ret(r["symbol"], t_open, r["entry"], 180, r["side"])
    # 出场后 60s:出场价 → 再往后走了多少(正=砍对了/出早了看方向)
    after = fwd_ret(r["symbol"], t_close, r["exit"], 60, r["side"])
    tot_fee += r.get("fee_usdt", 0)
    tot_real += r.get("realized_usdt", 0)
    rows.append((r["time"][11:19], r["symbol"], r["side"], hold, r["reason"].replace("fast_", ""),
                 r.get("realized_usdt", 0), r.get("fee_usdt", 0), delay, cost, cf60, cf180, after))
fmt = lambda v, w=6: (f"{v:+{w}.1f}" if isinstance(v, float) else " " * w) if v is not None else " " * w
print("time     symbol         side  hold reason           netU     feeU  delay entcost cf60   cf180  after60")
for row in rows:
    t_, s_, sd, h, rsn, net, fee, dl, c, c6, c18, af = row
    print(f"{t_} {s_:14s} {sd:5s} {h:3d}s {rsn:15s} {net:+.4f} {fee:.4f} {fmt(dl,5)} {fmt(c)} {fmt(c6)} {fmt(c18)} {fmt(af)}")
gross = tot_real + tot_fee
print(f"\n合计: 净={tot_real:+.4f}U 费={tot_fee:.4f}U 毛={gross:+.4f}U (费占毛亏比即费用拖累)")
cf60s = [x[9] for x in rows if x[9] is not None]
if cf60s:
    n5 = 5.0
    cf_net = sum(c / 10000 * n5 for c in cf60s)
    print(f"反事实(可算{len(cf60s)}笔): 若一律持有60s → 毛≈{cf_net:+.4f}U(双taker费另扣~{0.001*len(cf60s):.3f}U)")
cf180s = [x[10] for x in rows if x[10] is not None]
if cf180s:
    cf_net = sum(c / 10000 * 5.0 for c in cf180s)
    print(f"反事实(可算{len(cf180s)}笔): 若一律持有180s → 毛≈{cf_net:+.4f}U")
