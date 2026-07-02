#!/usr/bin/env python3
# fast 入场 edge 复盘(只读,不改实盘)。只用 clean 数据(maker_v3 + strict-fill/cleanup 恢复后 + seen-aware 可用)。
# 同时输出 gross(=realized+fee,扣费前)/ net(=realized)/ fee。目标:找 n 够、gross 为正的入场子集。
# 若无任何 gross 正子集 → 当前 fast 入场无 edge,exit/fee 修不成盈利。
import json, os, collections
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
P3 = datetime(2026, 7, 1, 15, 33, 25, tzinfo=timezone.utc)        # strict-fill/cleanup+seen-aware 恢复
EARLYCUT = datetime(2026, 7, 1, 15, 59, 8, tzinfo=timezone.utc)   # early-cut+cooldown 上线


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc)
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def load_clean():
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
            t = ts(d.get("time"))
            if t and t >= P3 and d.get("maker_entry") and d.get("post_fill_mfe_3s_bps") is not None:
                d["_t"] = t
                rows.append(d)
    return rows


def g(r):  # gross = realized + fee(扣费前)
    return r.get("realized_usdt", 0.0) + r.get("fee_usdt", 0.0)


def pf(xs):
    p = sum(x for x in xs if x > 0); n = sum(x for x in xs if x < 0)
    return (p / abs(n)) if n else (999.0 if p > 0 else 0.0)


def line(label, rows):
    if not rows:
        print(f"    {label:<24} n=0"); return
    gs = [g(r) for r in rows]; ns = [r.get("realized_usdt", 0.0) for r in rows]; fee = sum(r.get("fee_usdt", 0.0) for r in rows)
    fl = "" if len(rows) >= 20 else (" ⚠<20" if len(rows) >= 8 else " ⚠极少")
    mark = " ★gross+" if sum(gs) > 0 else ""
    print(f"    {label:<24} n={len(rows):<3} gross={sum(gs):+7.3f}(PF{pf(gs):.2f}) net={sum(ns):+7.3f} fee={fee:.3f} win_g={sum(1 for x in gs if x>0)/len(rows)*100:.0f}%{fl}{mark}")


def bucket(rows, keyfn, order):
    b = collections.defaultdict(list)
    for r in rows:
        b[keyfn(r)].append(r)
    for k in order:
        line(k, b.get(k, []))
    for k in sorted(b):
        if k not in order:
            line(k + "(其他)", b[k])


def isign(v, strong=20):
    if v is None:
        return "na"
    if v <= -strong: return "strong_neg"
    if v < -2: return "neg"
    if v < 2: return "flat"
    if v < strong: return "pos"
    return "strong_pos"


def run(name, rows):
    print("\n" + "#" * 96)
    print(f"### {name}  n={len(rows)}")
    print("#" * 96)
    line("总体", rows)
    if not rows:
        return
    print("\n[1. entry_impulse_30s 分桶]")
    bucket(rows, lambda r: isign(r.get("mid_impulse_30s_bps")), ["strong_neg", "neg", "flat", "pos", "strong_pos"])
    print("  [impulse 15s vs 30s 冲突]")
    line("同号", [r for r in rows if (r.get("mid_impulse_15s_bps", 0) or 0) * (r.get("mid_impulse_30s_bps", 0) or 0) > 0])
    line("冲突(异号)", [r for r in rows if (r.get("mid_impulse_15s_bps", 0) or 0) * (r.get("mid_impulse_30s_bps", 0) or 0) < 0])

    print("\n[2. post-fill early feedback: winner vs loser 的 MFE/MAE]")
    for lbl, sub in [("winner(net>0)", [r for r in rows if r.get("realized_usdt", 0) > 0]),
                     ("loser(net<0)", [r for r in rows if r.get("realized_usdt", 0) < 0])]:
        cells = []
        for w in [3, 5, 10]:
            mfe = [r[f"post_fill_mfe_{w}s_bps"] for r in sub if r.get(f"post_fill_mfe_{w}s_bps") is not None]
            mae = [r[f"post_fill_mae_{w}s_bps"] for r in sub if r.get(f"post_fill_mae_{w}s_bps") is not None]
            cells.append(f"{w}s={sum(mfe)/len(mfe):+.1f}/{sum(mae)/len(mae):+.1f}" if (mfe and mae) else f"{w}s=—")
        print(f"    {lbl:<14} n={len(sub):<3} " + "  ".join(cells))

    print("\n[3. maker_edge_bps 分桶]")
    bucket(rows, lambda r: (lambda e: "e<=0" if e <= 0 else ("0-0.5" if e < 0.5 else ("0.5-1" if e < 1 else ">=1")))(r.get("maker_edge_bps", 0)), ["e<=0", "0-0.5", "0.5-1", ">=1"])
    print("  [wait_ms]")
    bucket(rows, lambda r: (lambda w: "<500" if w < 500 else ("500-1k" if w < 1000 else ("1k-1.5k" if w < 1500 else ">1.5k")))(r.get("maker_wait_ms", 0)), ["<500", "500-1k", "1k-1.5k", ">1.5k"])
    print("  [entry spread_bps]")
    bucket(rows, lambda r: (lambda s: "<2" if s < 2 else ("2-4" if s < 4 else ">=4"))(r.get("entry_spread_bps", 0)), ["<2", "2-4", ">=4"])

    print("\n[4. orderbook imbalance 分桶]")
    bucket(rows, lambda r: (lambda i: "<-0.3" if i < -0.3 else ("-0.3~0.3" if i < 0.3 else "0.3-0.85" if i < 0.85 else ">=0.85"))(r.get("entry_imbalance", 0)), ["<-0.3", "-0.3~0.3", "0.3-0.85", ">=0.85"])

    print("\n[5. score 桶 + score_reason token(先看区分度)]")
    bucket(rows, lambda r: (lambda s: "0.6-0.7" if s < 0.7 else ("0.7-0.85" if s < 0.85 else ">=0.85"))(r.get("fast_candidate_score", 0)), ["0.6-0.7", "0.7-0.85", ">=0.85"])
    for tag in ["narrow_spread", "deep_book", "clear_imbalance", "impulse_cont", "impulse_partial", "imbalance_against_side", "extreme_imbalance_penalty"]:
        line("tok:" + tag, [r for r in rows if tag in (r.get("fast_candidate_reason") or "")])

    print("\n[6. side]")
    bucket(rows, lambda r: r.get("side", "?"), ["long", "short"])

    print("\n[7. symbol(n>=5,仅诊断)]")
    bs = collections.defaultdict(list)
    for r in rows:
        bs[r.get("symbol", "?")].append(r)
    for s in sorted(bs, key=lambda k: sum(g(x) for x in bs[k])):
        if len(bs[s]) >= 5:
            line(s, bs[s])

    print("\n[8. exit_reason]")
    bucket(rows, lambda r: r.get("reason", "?"), ["fast_trail_tp", "fast_early_cut", "fast_stop_reversal", "fast_no_feedback", "fast_max_hold"])

    print("\n[9. 单维 gross>0 子集扫描(n>=8)]")
    found = []
    dims = {
        "impulse30": lambda r: "imp30_" + isign(r.get("mid_impulse_30s_bps")),
        "edge": lambda r: "edge_" + ((lambda e: "le0" if e <= 0 else ("lt1" if e < 1 else "ge1"))(r.get("maker_edge_bps", 0))),
        "imb": lambda r: "imb_" + ((lambda i: "neg" if i < -0.3 else ("mid" if i < 0.3 else "pos"))(r.get("entry_imbalance", 0))),
        "side": lambda r: "side_" + r.get("side", "?"),
        "spread": lambda r: "spr_" + ((lambda s: "lt2" if s < 2 else "ge2")(r.get("entry_spread_bps", 0))),
    }
    for dn, kf in dims.items():
        b = collections.defaultdict(list)
        for r in rows:
            b[kf(r)].append(r)
        for k, sub in b.items():
            gs = sum(g(x) for x in sub)
            if gs > 0 and len(sub) >= 8:
                found.append((k, len(sub), gs, sum(x.get("realized_usdt", 0) for x in sub)))
    if found:
        for k, n, gs, ns in sorted(found, key=lambda x: -x[2]):
            print(f"    ★ {k:<20} n={n} gross={gs:+.3f} net={ns:+.3f}")
    else:
        print("    (无 n>=8 且 gross>0 的单维子集 → 该阶段入场无正毛利 edge)")


def main():
    rows = load_clean()
    print(f"clean fast(maker_v3 + P3后 + seen-aware) 总 n={len(rows)}")
    run("A. clean 全体(early-cut 前后合计)", rows)
    run("B. early-cut 上线前(P3→early-cut)", [r for r in rows if r["_t"] < EARLYCUT])
    run("C. early-cut 上线后", [r for r in rows if r["_t"] >= EARLYCUT])


if __name__ == "__main__":
    main()
