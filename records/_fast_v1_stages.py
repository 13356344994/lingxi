#!/usr/bin/env python3
# P4.1 fast_v1 分阶段复盘(只读,不改实盘)。
# 只有 maker_v3_clean / seen_aware_clean 的结果可指导下一步。全量只说明"历史 fast 总体亏"。
import json, os, collections
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
CLEAN = datetime(2026, 7, 1, 15, 33, 25, tzinfo=timezone.utc)  # P3 strict-fill/cleanup+seen-aware 恢复部署


def ts(s):
    s = (s or "").strip().rstrip("Z")
    if "." in s:
        h, f = s.split(".", 1); s = h + "." + "".join(c for c in f if c.isdigit())[:6]; fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    try:
        return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
    except ValueError:
        return None


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
                r = json.loads(l); r["_t"] = ts(r.get("time")); rows.append(r)
            except json.JSONDecodeError:
                pass
    return rows


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def stat(rows):
    nets = [r.get("realized_usdt", 0.0) for r in rows]
    w = [x for x in nets if x > 0]; l = [x for x in nets if x < 0]
    return dict(n=len(rows), net=sum(nets), pf=pf(nets),
                win=(len(w) / len(rows) * 100 if rows else 0),
                avgW=(sum(w) / len(w) if w else 0), avgL=(sum(l) / len(l) if l else 0),
                fee=sum(r.get("fee_usdt", 0.0) for r in rows))


def line(label, rows):
    if not rows:
        print(f"    {label:<20} n=0"); return
    s = stat(rows)
    fl = "" if s["n"] >= 20 else (" ⚠<20" if s["n"] >= 8 else " ⚠极少")
    print(f"    {label:<20} n={s['n']:<4} net={s['net']:+7.3f} PF={s['pf']:<5.2f} win={s['win']:>3.0f}% 均盈={s['avgW']:+.4f} 均亏={s['avgL']:+.4f} fee={s['fee']:.3f}{fl}")


def buckets(rows):
    def ek(r):
        e = r.get("maker_edge_bps", 0)
        return "e<=0" if e <= 0 else ("0-0.5" if e < 0.5 else ("0.5-1" if e < 1 else ">=1"))
    def wk(r):
        w = r.get("maker_wait_ms", 0)
        return "<500" if w < 500 else ("500-1k" if w < 1000 else ("1k-1.5k" if w < 1500 else ">1.5k"))
    def sk(r):
        s = r.get("fast_candidate_score", 0)
        return "0.6-0.7" if s < 0.7 else ("0.7-0.85" if s < 0.85 else ">=0.85")
    print("  exit_reason:")
    er = collections.defaultdict(list)
    for r in rows:
        er[r.get("reason", "?")].append(r)
    for k in ["fast_trail_tp", "fast_stop_reversal", "fast_no_feedback", "fast_max_hold", "fast_tp", "fast_horizon_exit", "fast_early_cut"]:
        if er.get(k):
            line("  " + k, er[k])
    mk = [r for r in rows if r.get("maker_entry")]
    if mk:
        print("  maker_edge:")
        b = collections.defaultdict(list)
        for r in mk:
            b[ek(r)].append(r)
        for k in ["e<=0", "0-0.5", "0.5-1", ">=1"]:
            line("  " + k, b.get(k, []))
        print("  wait_ms:")
        b = collections.defaultdict(list)
        for r in mk:
            b[wk(r)].append(r)
        for k in ["<500", "500-1k", "1k-1.5k", ">1.5k"]:
            line("  " + k, b.get(k, []))
    print("  score:")
    b = collections.defaultdict(list)
    for r in rows:
        b[sk(r)].append(r)
    for k in ["0.6-0.7", "0.7-0.85", ">=0.85"]:
        line("  " + k, b.get(k, []))
    print("  score_reason:")
    for tag in ["narrow_spread", "deep_book", "clear_imbalance", "impulse_cont", "imbalance_against_side"]:
        line("  " + tag, [r for r in rows if tag in (r.get("fast_candidate_reason") or "")])


def mfemae(rows):
    groups = {
        "winner": [r for r in rows if r.get("realized_usdt", 0) > 0],
        "loser": [r for r in rows if r.get("realized_usdt", 0) < 0],
        "stop_reversal": [r for r in rows if r.get("reason") == "fast_stop_reversal"],
        "trail_tp": [r for r in rows if r.get("reason") == "fast_trail_tp"],
    }
    wins = [3, 5, 10, 15]
    print("  MFE/MAE(bps) 结局   n   " + " ".join(f"{w}s" for w in wins))
    for g, rs in groups.items():
        cells = []
        for w in wins:
            mfe = [r[f"post_fill_mfe_{w}s_bps"] for r in rs if r.get(f"post_fill_mfe_{w}s_bps") is not None]
            mae = [r[f"post_fill_mae_{w}s_bps"] for r in rs if r.get(f"post_fill_mae_{w}s_bps") is not None]
            cells.append(f"{sum(mfe)/len(mfe):+.1f}/{sum(mae)/len(mae):+.1f}" if (mfe and mae) else "—")
        print(f"    {g:<13} {len(rs):<4} " + " ".join(f"{c:>11}" for c in cells))


def main():
    rows = load()
    print(f"P4.1 分阶段复盘  fast_v1 总 n={len(rows)}  clean分界={CLEAN.isoformat()}")
    stages = {
        "1.taker_fixed_60s": [r for r in rows if not r.get("maker_entry") and r.get("reason") == "fast_horizon_exit"],
        "2.taker_scalper_v2": [r for r in rows if not r.get("maker_entry") and r.get("reason") in ("fast_tp", "fast_stop_reversal", "fast_max_hold")],
        "3.maker_v3_dirty": [r for r in rows if r.get("maker_entry") and r["_t"] and r["_t"] < CLEAN],
        "4.maker_v3_clean": [r for r in rows if r.get("maker_entry") and r["_t"] and r["_t"] >= CLEAN],
        "5.seen_aware_clean": [r for r in rows if r["_t"] and r["_t"] >= CLEAN and r.get("post_fill_mfe_3s_bps") is not None],
    }
    for name, rs in stages.items():
        print("\n" + "=" * 90)
        print(f"阶段 {name}")
        print("=" * 90)
        line("阶段汇总", rs)
        if not rs:
            continue
        buckets(rs)
        mfemae(rs)
    print("\n判据:仅 4.maker_v3_clean / 5.seen_aware_clean 指导实盘;若其 winner/loser 在 3-5s 已分化 → 优先 early-cut。")


if __name__ == "__main__":
    main()
