#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""方向分类器研究(只读;头脑风暴①跨所价差锚的正式裁决脚本)。

问题:暴动(≥25bps@60s)之后,顺着走(follow)还是反着接(fade)?
分类器:Δgap 占比 = 运动窗内 OKX 相对锚的价差变化 / OKX 自身位移。
  - Δgap/move ≈ 1:锚没跟 = OKX 独走 = 本地流动性事故 → 套利商必来搬平 → 预测 fade;
  - Δgap/move ≈ 0:锚同步走 = 全局重定价 → 无人拽回 → 预测 follow。
信息集:全部特征在"检测时刻"可实时计算(锚有各自 ts,读端按 ts 对齐,无前视/无测量污染)。
数据:fast_hot_mids.jsonl 的干净段(2026-07-04T16:00Z 起:锚带 ts + side 映射修正后)。
用法:python3 _direction_classifier_study.py [--since 2026-07-04T16:00]
"""
import argparse
import json
import datetime
import bisect
import collections
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
ANCHORS = ("bybit", "binance", "gate", "okxspot")


def ts(s):
    s = s[:19]
    for fmt in ("%Y-%m-%dT%H:%M:%S", "%Y-%m-%dT%H:%M", "%Y-%m-%d"):
        try:
            return datetime.datetime.strptime(s, fmt)
        except ValueError:
            continue
    raise ValueError(f"bad time: {s}")


def load(since):
    """返回 okx 路径与锚路径(全部带真实时间戳,ts 对齐可靠)。"""
    okx = collections.defaultdict(list)              # sym -> [(t, mid)]
    anch = {v: collections.defaultdict(list) for v in ANCHORS}  # venue -> sym -> [(t, mid)]
    with open(os.path.join(DIR, "fast_hot_mids.jsonl")) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            t = ts(d["recorded_at"])
            if t < since:
                continue
            if d.get("backlog_sym"):
                sym = d["backlog_sym"]
                dst = anch.get(d.get("backlog_venue", ""), None)
                target = dst[sym] if dst is not None else okx[sym]
                for ms, mid in d.get("backlog", []):
                    target.append((datetime.datetime.utcfromtimestamp(ms / 1000), mid))
                continue
            for sym, mid in d.get("mids", {}).items():
                okx[sym].append((t, mid))
            for v, m in (d.get("anchors") or {}).items():
                if v not in anch:
                    continue
                for sym, val in m.items():
                    # [mid, ts_ms]:用锚自身时间戳(审计修正后的关键)
                    anch[v][sym].append((datetime.datetime.utcfromtimestamp(val[1] / 1000), val[0]))
    for m in [okx] + list(anch.values()):
        for s in m:
            m[s] = sorted(set(m[s]))
    return okx, anch


def at(series, t, tol):
    """<=t 的最近点(tol 秒内)。"""
    if not series:
        return None
    keys = [x[0] for x in series]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or (t - series[i][0]).total_seconds() > tol:
        return None
    return series[i][1]


def fwd(series, t, m0, secs, tol=40):
    if not series or not m0:
        return None
    keys = [x[0] for x in series]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(series) or (series[i][0] - t).total_seconds() > secs + tol:
        return None
    return (series[i][1] - m0) / m0 * 10000


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--since", default="2026-07-04T16:00")
    ap.add_argument("--move-bps", type=float, default=25.0)
    args = ap.parse_args()
    since = ts(args.since)
    okx, anch = load(since)
    print(f"干净段: {args.since}Z 起 | okx 路径 {len(okx)} 币 | 锚路径覆盖 " +
          " ".join(f"{v}:{len(anch[v])}" for v in ANCHORS))

    # ---- 检测暴动(与普查同口径:60s 窗 |位移|≥move_bps,120s 去重)----
    events = []
    for sym, arr in okx.items():
        if len(arr) < 30:
            continue
        keys = [x[0] for x in arr]
        last = None
        for i in range(len(arr)):
            t0, m0 = arr[i]
            j = bisect.bisect_right(keys, t0 - datetime.timedelta(seconds=60)) - 1
            if j < 0 or (t0 - arr[j][0]).total_seconds() > 100:
                continue
            mprev = arr[j][1]
            move = (m0 - mprev) / mprev * 10000
            if abs(move) < args.move_bps:
                continue
            if last and (t0 - last).total_seconds() < 120:
                continue
            last = t0
            events.append((sym, t0, m0, arr[j][0], mprev, move))
    print(f"暴动事件(|60s|≥{args.move_bps:.0f}bps): {len(events)}")

    # ---- 分类:Δgap 占比(锚共识=可用锚的中位数)----
    rows = []
    n_no_anchor = 0
    for sym, t0, m0, tp, mp, move in events:
        ratios = []
        for v in ANCHORS:
            a_now = at(anch[v].get(sym, []), t0, 10)
            a_prev = at(anch[v].get(sym, []), tp, 10)
            if a_now is None or a_prev is None or a_prev <= 0:
                continue
            a_move = (a_now - a_prev) / a_prev * 10000  # 锚同窗位移
            ratios.append((move - a_move) / move)       # 1=锚没跟(独走) 0=锚全跟(全局)
        if not ratios:
            n_no_anchor += 1
            continue
        ratios.sort()
        ratio = ratios[len(ratios) // 2]
        sgn = 1 if move > 0 else -1
        f60 = fwd(okx[sym], t0, m0, 60)
        f180 = fwd(okx[sym], t0, m0, 180)
        if f60 is None:
            continue
        rows.append(dict(sym=sym, t=t0, move=move, ratio=ratio, n_anchor=len(ratios),
                         fol60=sgn * f60, fol180=(sgn * f180 if f180 is not None else None)))
    print(f"可分类(≥1锚可对齐): {len(rows)} | 无锚覆盖: {n_no_anchor}")

    def rep(tag, sub):
        v = [r["fol60"] for r in sub]
        if len(v) < 5:
            print(f"  {tag:34s} n={len(v)}(太少)")
            return
        v180 = [r["fol180"] for r in sub if r["fol180"] is not None]
        pos = sum(1 for x in v if x > 0)
        m = sorted(v)[len(v) // 2]
        syms = collections.Counter(r["sym"] for r in sub)
        l = ""
        if v180:
            l = f" | 180s均={sum(v180)/len(v180):+6.1f} 中={sorted(v180)[len(v180)//2]:+6.1f}"
        print(f"  {tag:34s} n={len(v):4d} 顺向60s: 延续率={pos/len(v)*100:3.0f}% 均={sum(v)/len(v):+6.1f} 中={m:+6.1f}{l} top:{dict(syms.most_common(2))}")

    print("\n== 分类器判决(follow 口径:正=延续有肉,负=该 fade) ==")
    rep("独走(ratio≥0.7,预测fade)", [r for r in rows if r["ratio"] >= 0.7])
    rep("半独走(0.3-0.7)", [r for r in rows if 0.3 <= r["ratio"] < 0.7])
    rep("全局同走(ratio<0.3,预测follow)", [r for r in rows if r["ratio"] < 0.3])
    print("\n== 稳健性 ==")
    multi = [r for r in rows if r["n_anchor"] >= 2]
    rep("独走·多锚确认", [r for r in multi if r["ratio"] >= 0.7])
    rep("全局·多锚确认", [r for r in multi if r["ratio"] < 0.3])
    for tag, sub in [("独走", [r for r in rows if r["ratio"] >= 0.7]),
                     ("全局", [r for r in rows if r["ratio"] < 0.3])]:
        if len(sub) >= 10:
            top = collections.Counter(r["sym"] for r in sub).most_common(1)[0][0]
            rep(f"{tag}·剔除{top}", [r for r in sub if r["sym"] != top])


if __name__ == "__main__":
    main()
