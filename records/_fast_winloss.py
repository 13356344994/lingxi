#!/usr/bin/env python3
# fast winner-vs-loser 分类研究(只读)。
# positive class = fast_trail_tp / fast_tp ; negative class = fast_stop_reversal / fast_no_feedback。
# 比较两类在【入场前】特征的分布 + 效应量(|Cohen d|),找能提前预测 positive 的特征。
# 若所有入场特征 |d| 都很小(<~0.2)→ 现有字段无法预测 → 需采新字段(trade flow / book delta / lead-lag / liquidity refill)。
import json, os, math, collections

VEN = ["okx", "bybit", "binance", "gate"]
POS = {"fast_trail_tp", "fast_tp"}
NEG = {"fast_stop_reversal", "fast_no_feedback"}


def load():
    rows = []
    for v in VEN:
        f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            if '"signal_layer":"fast_v1"' not in l:
                continue
            try:
                d = json.loads(l)
            except json.JSONDecodeError:
                continue
            if d.get("maker_entry") and d.get("reason") in (POS | NEG):
                rows.append(d)
    return rows


def dirsign(r):
    return 1.0 if r.get("side") == "long" else -1.0


def feats(r):
    s = dirsign(r)
    return {
        "score": r.get("fast_candidate_score", 0.0),
        "maker_edge": r.get("maker_edge_bps", 0.0),
        "maker_wait_ms": float(r.get("maker_wait_ms", 0)),
        "spread": r.get("entry_spread_bps", 0.0),
        "imbalance_raw": r.get("entry_imbalance", 0.0),
        "imbalance_dir": r.get("entry_imbalance", 0.0) * s,   # 朝持仓方向(买盘厚支持多)
        "abs_imbalance": abs(r.get("entry_imbalance", 0.0)),
        "impulse15": r.get("mid_impulse_15s_bps", 0.0),
        "impulse30": r.get("mid_impulse_30s_bps", 0.0),
        "impulse60": r.get("mid_impulse_60s_bps", 0.0),
        "impulse30_dir": r.get("mid_impulse_30s_bps", 0.0) * s,  # 朝持仓方向的动量
        "impulse15_dir": r.get("mid_impulse_15s_bps", 0.0) * s,
    }


def stats(xs):
    n = len(xs)
    if n == 0:
        return 0, 0, 0
    m = sum(xs) / n
    var = sum((x - m) ** 2 for x in xs) / n if n > 1 else 0
    return m, math.sqrt(var), n


def main():
    rows = load()
    pos = [r for r in rows if r.get("reason") in POS]
    neg = [r for r in rows if r.get("reason") in NEG]
    print(f"maker fast winner/loser: positive(trail_tp/tp)={len(pos)}  negative(stop/no_feedback)={len(neg)}  总={len(rows)}")
    print("=" * 96)
    print("\n【数值入场特征:pos vs neg 均值 + |Cohen d|(按 |d| 排序;|d|>=0.2 才算有区分)】")
    keys = ["impulse30_dir", "impulse15_dir", "imbalance_dir", "impulse30", "impulse15", "impulse60",
            "maker_edge", "score", "spread", "maker_wait_ms", "abs_imbalance", "imbalance_raw"]
    res = []
    for k in keys:
        pm, ps, pn = stats([feats(r)[k] for r in pos])
        nm, ns, nn = stats([feats(r)[k] for r in neg])
        pooled = math.sqrt((ps ** 2 + ns ** 2) / 2) or 1e-9
        d = (pm - nm) / pooled
        res.append((abs(d), k, pm, nm, d))
    for ad, k, pm, nm, d in sorted(res, reverse=True):
        flag = "  ★有区分" if ad >= 0.2 else ("  (弱)" if ad >= 0.1 else "  无")
        print(f"    {k:<16} pos均={pm:+8.3f}  neg均={nm:+8.3f}  d={d:+.3f}{flag}")

    print("\n【类别特征:各取值的 positive 占比(pos/(pos+neg))】")
    def posrate(keyfn, order=None, minn=8):
        b = collections.defaultdict(lambda: [0, 0])
        for r in rows:
            k = keyfn(r)
            b[k][0 if r.get("reason") in POS else 1] += 1
        ks = order or sorted(b, key=lambda x: -(b[x][0] + b[x][1]))
        for k in ks:
            p, n = b[k]
            if p + n >= minn:
                print(f"      {str(k):<22} n={p+n:<4} pos率={p/(p+n)*100:.0f}%")
    print("  side:")
    posrate(lambda r: r.get("side"), ["long", "short"])
    print("  score桶:")
    posrate(lambda r: "0.6-0.7" if r.get("fast_candidate_score", 0) < 0.7 else ("0.7-0.85" if r.get("fast_candidate_score", 0) < 0.85 else ">=0.85"), ["0.6-0.7", "0.7-0.85", ">=0.85"])
    print("  score_reason token:")
    for tag in ["narrow_spread", "deep_book", "clear_imbalance", "impulse_cont", "impulse_partial", "imbalance_against_side", "extreme_imbalance_penalty"]:
        sub = [r for r in rows if tag in (r.get("fast_candidate_reason") or "")]
        p = sum(1 for r in sub if r.get("reason") in POS)
        if sub:
            print(f"      {tag:<24} n={len(sub):<4} pos率={p/len(sub)*100:.0f}%")
    print("  open hour(UTC):")
    def hr(r):
        t = (r.get("time") or "")[11:13]
        return t
    posrate(hr, [f"{h:02d}" for h in range(24)], minn=15)
    print("  symbol(n>=15,仅诊断):")
    posrate(lambda r: r.get("symbol"), None, minn=15)

    print("\n【post-fill MFE/MAE(bps)pos vs neg(确认分化在几秒发生)】")
    for lbl, sub in [("positive", pos), ("negative", neg)]:
        cells = []
        for w in [3, 5, 10]:
            mfe = [r[f"post_fill_mfe_{w}s_bps"] for r in sub if r.get(f"post_fill_mfe_{w}s_bps") is not None]
            mae = [r[f"post_fill_mae_{w}s_bps"] for r in sub if r.get(f"post_fill_mae_{w}s_bps") is not None]
            cells.append(f"{w}s={sum(mfe)/len(mfe):+.1f}/{sum(mae)/len(mae):+.1f}" if (mfe and mae) else f"{w}s=—")
        print(f"    {lbl:<10} n={len(sub):<4} " + "  ".join(cells))

    print("\n结论判据:若上面数值特征 |d| 全 <0.2 且类别 pos率 都接近整体 → 现有入场字段无法提前区分 → 需采新字段。")
    overall = len(pos) / max(1, len(rows)) * 100
    print(f"  整体 positive 占比={overall:.0f}%(作为各桶对比基线)")


if __name__ == "__main__":
    main()
