#!/usr/bin/env python3
# P4 fast_v1 实盘结算复盘(只读)。不改实盘。
# 目标:找 scorer 误判的通用特征(不把"亏损币"当根因)。
# 分桶:maker_edge / maker_wait_ms / score / exit_reason / symbol / seen-aware MFE-MAE / score_reason。
import json, os, collections

VEN = ["okx", "bybit", "binance", "gate"]


def load():
    rows = []
    for v in VEN:
        f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            l = l.strip()
            if not l or '"signal_layer":"fast_v1"' not in l:
                continue
            try:
                rows.append(json.loads(l))
            except json.JSONDecodeError:
                pass
    return rows


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def stat(rows):
    nets = [r.get("realized_usdt", 0.0) for r in rows]
    w = [x for x in nets if x > 0]; l = [x for x in nets if x < 0]
    fee = sum(r.get("fee_usdt", 0.0) for r in rows)
    return dict(n=len(rows), net=sum(nets), pf=pf(nets),
                win=(len(w) / len(rows) * 100 if rows else 0),
                avgW=(sum(w) / len(w) if w else 0), avgL=(sum(l) / len(l) if l else 0), fee=fee)


def line(label, rows):
    if not rows:
        print(f"    {label:<22} n=0"); return
    s = stat(rows)
    flag = "" if s["n"] >= 20 else (" ⚠<20" if s["n"] >= 8 else " ⚠极少")
    print(f"    {label:<22} n={s['n']:<4} net={s['net']:+7.3f} PF={s['pf']:<5.2f} win={s['win']:>3.0f}% "
          f"均盈={s['avgW']:+.4f} 均亏={s['avgL']:+.4f} fee={s['fee']:.3f}{flag}")


def bucket(rows, keyfn, order):
    b = collections.defaultdict(list)
    for r in rows:
        b[keyfn(r)].append(r)
    for k in order:
        line(str(k), b.get(k, []))
    for k in sorted(b):
        if k not in order:
            line(str(k) + "(其他)", b[k])


def main():
    rows = load()
    print("=" * 100)
    print(f"P4 fast_v1 实盘复盘(只读)  总 n={len(rows)}")
    if not rows:
        print("无 fast_v1 结算。"); return
    maker = [r for r in rows if r.get("maker_entry")]
    print(f"  其中 maker 入场={len(maker)}  taker/旧={len(rows)-len(maker)}")
    line("全体", rows)
    if maker:
        line("仅maker入场", maker)
    print("=" * 100)

    print("\n【1. maker_edge_bps 分桶(仅maker)】")
    def ek(r):
        e = r.get("maker_edge_bps", 0)
        if e <= 0: return "edge<=0"
        if e < 0.5: return "0<edge<0.5"
        if e < 1: return "0.5<=edge<1"
        return "edge>=1"
    bucket(maker, ek, ["edge<=0", "0<edge<0.5", "0.5<=edge<1", "edge>=1"])

    print("\n【2. maker_wait_ms 分桶(仅maker)】")
    def wk(r):
        w = r.get("maker_wait_ms", 0)
        if w < 500: return "<500"
        if w < 1000: return "500-1000"
        if w < 1500: return "1000-1500"
        return ">1500"
    bucket(maker, wk, ["<500", "500-1000", "1000-1500", ">1500"])

    print("\n【3. score 分桶】")
    def sk(r):
        s = r.get("fast_candidate_score", 0)
        if s < 0.7: return "0.6-0.7"
        if s < 0.85: return "0.7-0.85"
        return ">=0.85"
    bucket(rows, sk, ["0.6-0.7", "0.7-0.85", ">=0.85"])

    print("\n【4. exit_reason 分桶】")
    bucket(rows, lambda r: r.get("reason", "?"),
           ["fast_trail_tp", "fast_stop_reversal", "fast_no_feedback", "fast_max_hold", "fast_tp", "fast_horizon_exit"])

    print("\n【5. symbol 分桶(n>=5)】")
    bysym = collections.defaultdict(list)
    for r in rows:
        bysym[r.get("symbol", "?")].append(r)
    for sym in sorted(bysym, key=lambda k: stat(bysym[k])["net"]):
        if len(bysym[sym]) >= 5:
            line(sym, bysym[sym])

    print("\n【6. seen-aware MFE/MAE(bps)× 结局:winner/loser/stop_reversal/trail_tp】")
    groups = {
        "winner": [r for r in rows if r.get("realized_usdt", 0) > 0],
        "loser": [r for r in rows if r.get("realized_usdt", 0) < 0],
        "stop_reversal": [r for r in rows if r.get("reason") == "fast_stop_reversal"],
        "trail_tp": [r for r in rows if r.get("reason") == "fast_trail_tp"],
        "no_feedback": [r for r in rows if r.get("reason") == "fast_no_feedback"],
    }
    wins = [3, 5, 10, 15, 30, 60]
    print("    结局         n    " + "  ".join(f"{w}s(MFE/MAE)" for w in wins))
    for gname, g in groups.items():
        cells = []
        for w in wins:
            mfe = [r[f"post_fill_mfe_{w}s_bps"] for r in g if r.get(f"post_fill_mfe_{w}s_bps") is not None]
            mae = [r[f"post_fill_mae_{w}s_bps"] for r in g if r.get(f"post_fill_mae_{w}s_bps") is not None]
            if mfe:
                cells.append(f"{sum(mfe)/len(mfe):+.1f}/{sum(mae)/len(mae):+.1f}")
            else:
                cells.append("—")
        print(f"    {gname:<12} {len(g):<4} " + "  ".join(f"{c:>12}" for c in cells))

    print("\n【7. score_reason 分桶(含该 reason 的仓;可重叠)】")
    for tag in ["narrow_spread", "deep_book", "clear_imbalance", "impulse_cont", "impulse_partial", "imbalance_against_side", "extreme_imbalance_penalty"]:
        line(tag, [r for r in rows if tag in (r.get("fast_candidate_reason") or "")])
    print("\n目标:找 scorer 误判的通用特征(edge/wait/impulse/deep_book),不做永久黑名单。")


if __name__ == "__main__":
    main()
