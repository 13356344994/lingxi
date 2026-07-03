#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""v3 蓝图每日裁决(只读):池塘核对 + 接型候选前向成绩 + 灰度是否达标,一条命令出结论。

用法: python3 _fast_v3_daily_verdict.py [--hours 24]

裁决口径(2026-07-03 定,禁止临时改标准):
  ① 池塘: 在线滚动判定(pond_ok) vs 实盘结算反事实(REJ 池挡掉多少亏损);
  ② fade_exhaustion: 池塘OK子集 +60s 均值 ≥ +1.5bps 且 +180s ≥ +3bps → 具备灰度入场设计条件;
  ③ liq_catch: 已证伪(07-03 首日,三桶全负);此处持续跟踪其镜像 liq_follow(顺瀑布)与
     吸收条件桶(imp30 已转向接的方向 = P2 那批 82% 的等价条件),确认解剖结论是否稳定。
"""
import argparse
import bisect
import datetime
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
SHADOW = os.path.join(HERE, "okx_directional", "fast_executable_shadow_events.jsonl")
SETTLE = os.path.join(HERE, "okx_directional", "fast_v2_settlements.jsonl")
WINDOWS = (15, 30, 60, 180)


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


def load(cut):
    base, alts, pond_latest, sid2pond = {}, [], {}, {}
    with open(SHADOW) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            t = ts(d["recorded_at"])
            if t < cut:
                continue
            if d.get("candidate_source"):
                alts.append((t, d))
                continue
            base.setdefault(d["symbol"], []).append((t, d["mid"]))
            pond_latest[d["symbol"]] = d
            if d.get("snapshot_id"):
                sid2pond[d["snapshot_id"]] = d
    for s in base:
        base[s].sort()
    return base, alts, pond_latest, sid2pond


def fwd(base, sym, t, mid, secs):
    arr = base.get(sym, [])
    keys = [x[0] for x in arr]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(arr):
        return None
    tt, m = arr[i]
    if (tt - t).total_seconds() > secs + 30:
        return None
    return (m - mid) / mid * 1e4


def bucket(base, evs, cond, label, flip=False):
    st = {}
    for t, d in evs:
        if not cond(d):
            continue
        sgn = 1 if d["fast_candidate_side"] == "long" else -1
        if flip:
            sgn = -sgn
        for w in WINDOWS:
            f = fwd(base, d["symbol"], t, d["mid"], w)
            if f is None:
                continue
            s = st.setdefault(w, {"n": 0, "fl": 0, "sum": 0.0})
            s["n"] += 1
            s["sum"] += sgn * f
            if sgn * f > 0:
                s["fl"] += 1
    print(label)
    out = {}
    for w in WINDOWS:
        s = st.get(w)
        if not s or s["n"] < 10:
            continue
        mean = s["sum"] / s["n"]
        out[w] = mean
        print("  +%3ds n=%5d 跟随=%.1f%% 均值=%+.2fbps" % (w, s["n"], 100.0 * s["fl"] / s["n"], mean))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--hours", type=float, default=24)
    args = ap.parse_args()
    cut = datetime.datetime.utcnow() - datetime.timedelta(hours=args.hours)
    print("裁决窗口: 最近 %.1f 小时(UTC %s 起)\n" % (args.hours, cut.strftime("%m-%d %H:%M")))
    base, alts, pond_latest, sid2pond = load(cut)

    # ① 池塘
    print("== ① 池塘判定(在线滚动中位,样本≥150 为成熟) ==")
    ok, rej = [], []
    for sym, d in sorted(pond_latest.items(), key=lambda kv: -kv[1].get("pond_energy_med_bps", 0)):
        mature = d.get("pond_samples", 0) >= 150
        (ok if d.get("pond_ok") else rej).append(sym) if mature else None
        print("%-16s 能量=%6.1f 深度=%9.0f 样本=%4d %s" % (
            sym, d.get("pond_energy_med_bps", 0), d.get("pond_depth_med_usdt", 0),
            d.get("pond_samples", 0),
            ("OK" if d.get("pond_ok") else "REJ") if mature else "热身"))
    print("成熟判定: OK=%d %s | REJ=%d" % (len(ok), ok, len(rej)))

    # 反事实:实盘结算按池塘分桶
    ok_p = rej_p = warm_p = 0.0
    ok_n = rej_n = warm_n = 0
    if os.path.exists(SETTLE):
        with open(SETTLE) as f:
            for line in f:
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if ts(d["time"]) < cut:
                    continue
                p = d.get("realized_usdt", 0)
                info = sid2pond.get(d.get("fast_event_id"))
                if info is None:
                    continue
                if info.get("pond_samples", 0) < 150:
                    warm_p += p; warm_n += 1
                elif info.get("pond_ok"):
                    ok_p += p; ok_n += 1
                else:
                    rej_p += p; rej_n += 1
    print("实盘反事实: OK池 %d笔 %+.4fU | REJ池 %d笔 %+.4fU(过滤开启后消失) | 热身 %d笔 %+.4fU\n"
          % (ok_n, ok_p, rej_n, rej_p, warm_n, warm_p))

    # ② fade
    fade = [(t, d) for t, d in alts if d["candidate_source"] == "fade_exhaustion"]
    def pond_ok_ev(d):
        return d.get("pond_ok") and d.get("pond_samples", 0) >= 150
    m_all = bucket(base, fade, lambda d: True, "== ② fade_exhaustion 全量 ==")
    m_ok = bucket(base, fade, pond_ok_ev, "== ② fade_exhaustion 池塘OK子集(灰度判据) ==")
    pass60 = m_ok.get(60, -99) >= 1.5
    pass180 = m_ok.get(180, -99) >= 3.0
    print("灰度判据: +60s≥+1.5bps → %s | +180s≥+3bps → %s | 结论: %s\n" % (
        "过" if pass60 else "不过", "过" if pass180 else "不过",
        "✅ 具备灰度入场设计条件" if (pass60 and pass180) else "⏳ 继续攒样本,不动"))

    # ③ liq_catch 解剖跟踪
    liq = [(t, d) for t, d in alts if d["candidate_source"] == "liq_catch"]
    bucket(base, liq, lambda d: True, "== ③ liq_catch 原样(已证伪,跟踪是否稳定) ==")
    bucket(base, liq, lambda d: True, "== ③ liq_follow 镜像(顺瀑布方向) ==", flip=True)
    def absorbed(d):
        i30 = d.get("mid_impulse_30s_bps", 0)
        return i30 >= 1 if d["fast_candidate_side"] == "long" else i30 <= -1
    bucket(base, liq, absorbed, "== ③ liq_catch 吸收桶(imp30已转向接的方向=P2等价条件) ==")


if __name__ == "__main__":
    main()
