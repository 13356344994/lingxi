#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""v3 蓝图前两层离线研究(只读,零学费):池塘质量标准 + 入场端物种预测。

对应 2026-07-03 与用户对齐的五层 v3 蓝图:
  ① 战场:只做"有人的池塘"(深度 5-60k 有水不是海 / 有能量非死水镜面 / 长尾优先);
  ② 入场:预判"谁接下来被迫/忍不住"——
     - FOMO 测试:候选方向 顺 30s 已走冲量(追)vs 逆(接/做套牢者对手盘)→ 前向跟随差;
     - 强平邻近(P2 首验):候选前 60s 同币有强平,顺强平方向(跟踪踩踏)vs 逆(接被迫者尾巴);
  锚距因子已在 _fast_motive_factors.py 验过(高分内 8.5pp 分层),不重复。

数据:fast_executable_shadow_events.jsonl(候选)、fast_dynamic_feature_events.jsonl(tick mid)、
      fast_liquidation_events.jsonl(强平,2026-07-03 04:13 UTC 起才有)。
跟随口径:方向 60s 内 MFE>=6bps(与全天分析一致)。仅相对分层有效,绝对值偏高勿直读。
"""
import bisect
import json
import os
from collections import defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))
LIQ = os.path.join(HERE, "okx_directional", "fast_liquidation_events.jsonl")

# 复用 motive factors 的加载与前向计算
src = open(os.path.join(HERE, "_fast_motive_factors.py")).read()
ns = {"__name__": "lib", "__file__": os.path.join(HERE, "_fast_motive_factors.py")}
exec(compile(src.split("def main()")[0], "mf", "exec"), ns)

FOLLOW = 6.0


def follow_rate(sel):
    if not sel:
        return 0.0, 0.0
    return (sum(1 for r in sel if r["mfe60"] >= FOLLOW) / len(sel),
            sum(r["mfe60"] for r in sel) / len(sel))


def mid_before(series, sym, ts, back_s):
    pair = series.get(sym)
    if not pair:
        return None
    ts_l, mid_l = pair
    i = bisect.bisect_right(ts_l, ts - back_s) - 1
    if i < 0 or (ts - back_s) - ts_l[i] > 10.0:
        return None
    return mid_l[i]


def main():
    print("[1/4] 载入候选与 tick …")
    cands = ns["load_candidates"]()
    syms = set(c["sym"] for c in cands)
    series = ns["load_mid_series"](syms)
    rows = []
    for c in cands:
        fm = ns["forward_metrics"](series, c["sym"], c["ts"], c["mid"], c["side"])
        if fm is None:
            continue
        c["mfe60"], c["ret30"] = fm
        m30 = mid_before(series, c["sym"], c["ts"], 30.0)
        c["pre30_bps"] = (c["mid"] - m30) / m30 * 1e4 if m30 else None
        rows.append(c)
    print("  有效样本 %d" % len(rows))

    # ========== ① 池塘质量标准 ==========
    print("\n===== ① 池塘质量(逐 symbol) =====")
    bysym = defaultdict(list)
    for r in rows:
        bysym[r["sym"]].append(r)
    stats = []
    for sym, g in bysym.items():
        if len(g) < 300:
            continue
        pf, mfe = follow_rate(g)
        depths = sorted(r["depth10"] for r in g)
        spreads = sorted(r["spread"] for r in g)
        moves = sorted(abs(r["ret30"]) for r in g)
        stats.append({
            "sym": sym, "n": len(g), "pf": pf, "mfe": mfe,
            "depth_med": depths[len(depths) // 2],
            "spread_med": spreads[len(spreads) // 2],
            "energy_med": moves[len(moves) // 2],  # 30s 实际位移中位 = 池塘能量
            "stock": sym in ns["STOCK_PERPS"],
        })
    stats.sort(key=lambda s: -s["pf"])
    print("%-16s %5s %7s %7s %9s %8s %7s %s" % ("symbol", "n", "P跟随", "MFE60", "深度中位", "点差中位", "能量30s", "类型"))
    for s in stats:
        print("%-16s %5d %6.1f%% %7.1f %9.0f %8.2f %7.2f %s" % (
            s["sym"], s["n"], s["pf"] * 100, s["mfe"], s["depth_med"],
            s["spread_med"], s["energy_med"], "股票" if s["stock"] else "币"))
    # 池塘标准反事实:深度带 + 能量下限
    good = [r for r in rows if 5000 <= r["depth10"] < 60000]
    inpond = set(s["sym"] for s in stats if s["energy_med"] >= 2.0)
    good_e = [r for r in good if r["sym"] in inpond]
    pf_all, _ = follow_rate(rows)
    pf_g, _ = follow_rate(good)
    pf_ge, _ = follow_rate(good_e)
    print("\n池塘标准反事实: 全部 %.1f%% → 深度5-60k %.1f%%(n=%d) → +能量中位>=2bps %.1f%%(n=%d)"
          % (pf_all * 100, pf_g * 100, len(good), pf_ge * 100, len(good_e)))

    # ========== ② FOMO:追 vs 接 ==========
    print("\n===== ② FOMO 测试(30s 已走冲量,追=顺 / 接=逆) =====")
    ok = [r for r in rows if r["pre30_bps"] is not None]
    for lo, hi, tag in ((3, 8, "冲量3-8bp"), (8, 20, "冲量8-20bp"), (20, 1e9, "冲量>=20bp")):
        moved = [r for r in ok if lo <= abs(r["pre30_bps"]) < hi]
        chase = [r for r in moved if (r["pre30_bps"] > 0) == (r["side"] == "long")]
        fade = [r for r in moved if (r["pre30_bps"] > 0) != (r["side"] == "long")]
        pc, mc = follow_rate(chase)
        pf_, mf_ = follow_rate(fade)
        print("  %-12s 追: n=%5d %5.1f%% MFE%5.1f | 接: n=%5d %5.1f%% MFE%5.1f"
              % (tag, len(chase), pc * 100, mc, len(fade), pf_ * 100, mf_))

    # ========== ③ 强平邻近(P2 首验,样本会很薄) ==========
    print("\n===== ③ 强平邻近 P2 首验(候选前 60s 同币有强平) =====")
    liq = []
    if os.path.exists(LIQ):
        for l in open(LIQ):
            try:
                d = json.loads(l)
                liq.append((d["symbol"], ns["parse_ts"](d["recorded_at"]),
                            d.get("pos_side", "")))
            except Exception:
                continue
    print("  强平事件 %d 条" % len(liq))
    liq_by_sym = defaultdict(list)
    for sym, ts, ps in liq:
        liq_by_sym[sym].append((ts, ps))
    for v in liq_by_sym.values():
        v.sort()
    liq_start = min((ts for _, ts, _ in liq), default=0)
    recent = [r for r in rows if r["ts"] >= liq_start]
    print("  强平窗内候选 %d 条" % len(recent))
    with_liq, without = [], []
    for r in recent:
        evs = liq_by_sym.get(r["sym"], [])
        i = bisect.bisect_left(evs, (r["ts"],))
        near = [e for e in evs[max(0, i - 8):i] if 0 <= r["ts"] - e[0] <= 60]
        if near:
            r["liq_ps"] = near[-1][1]
            with_liq.append(r)
        else:
            without.append(r)
    p0, m0 = follow_rate(without)
    print("  无强平邻近: n=%d P跟随=%.1f%% MFE=%.1f" % (len(without), p0 * 100, m0))
    if with_liq:
        # 多头被爆(强制卖压)后:short=顺踩踏 / long=接被迫者
        cascade = [r for r in with_liq if (r["liq_ps"] == "long") == (r["side"] == "short")]
        catch = [r for r in with_liq if (r["liq_ps"] == "long") != (r["side"] == "short")]
        p1, m1 = follow_rate(cascade)
        p2, m2 = follow_rate(catch)
        print("  顺踩踏方向: n=%d P跟随=%.1f%% MFE=%.1f" % (len(cascade), p1 * 100, m1))
        print("  接被迫者:   n=%d P跟随=%.1f%% MFE=%.1f" % (len(catch), p2 * 100, m2))
        print("  (n<100 只作趋势观察,持续积累后复跑)")


if __name__ == "__main__":
    main()
