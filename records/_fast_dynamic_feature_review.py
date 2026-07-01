#!/usr/bin/env python3
# fast 动态特征复盘(只读,不改实盘)。
# 对齐 fast_dynamic_feature_events(入场时刻附近)与 fast settlements 的胜负结局。
# positive=fast_trail_tp/fast_tp ; negative=fast_stop_reversal/fast_no_feedback。
# 每个动态字段输出 Cohen d / pos率 / gross PF / net PF / n;找 n>=100 & grossPF>1.3 & netPF>1.05 的入场子集。
# 注意:只有 WS 采集器上线后、且新 fast 仓入场时刻有邻近 dynamic event 才能对齐;上线初期样本少属正常。
import json, os, math, bisect
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
POS = {"fast_trail_tp", "fast_tp"}
NEG = {"fast_stop_reversal", "fast_no_feedback"}
ALIGN_TOL = 2.5  # 秒:入场时刻 ± 该值内取最近 dynamic event

NUM_FIELDS = [
    "trade_imbalance_1s", "trade_imbalance_3s", "trade_imbalance_5s",
    "trade_count_1s", "trade_count_5s", "large_trade_notional",
    "best_bid_qty_delta_3s", "best_ask_qty_delta_3s",
    "book_imbalance_now", "book_imbalance_delta_3s",
    "liquidity_pull_bid", "liquidity_pull_ask", "liquidity_refill_bid", "liquidity_refill_ask",
    "mid_ret_1s", "mid_ret_3s", "mid_ret_5s", "mid_ret_10s",
]


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc)
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def load_dyn():
    by = {}
    f = "okx_directional/fast_dynamic_feature_events.jsonl"
    if not os.path.exists(f):
        return by
    for l in open(f):
        l = l.strip()
        if not l:
            continue
        try:
            d = json.loads(l)
        except json.JSONDecodeError:
            continue
        t = ts(d.get("recorded_at"))
        if not t:
            continue
        by.setdefault(d.get("symbol"), []).append((t.timestamp(), d))
    for s in by:
        by[s].sort(key=lambda x: x[0])
    return by


def load_settle():
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
            if d.get("reason") in (POS | NEG):
                rows.append(d)
    return rows


def align(dyn, sym, t):
    arr = dyn.get(sym)
    if not arr or t is None:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_left(keys, t.timestamp())
    best = None
    for j in (i - 1, i):
        if 0 <= j < len(arr):
            dt = abs(arr[j][0] - t.timestamp())
            if dt <= ALIGN_TOL and (best is None or dt < best[0]):
                best = (dt, arr[j][1])
    return best[1] if best else None


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def cohen(a, b):
    if len(a) < 2 or len(b) < 2:
        return 0.0
    ma, mb = sum(a) / len(a), sum(b) / len(b)
    va = sum((x - ma) ** 2 for x in a) / len(a)
    vb = sum((x - mb) ** 2 for x in b) / len(b)
    p = math.sqrt((va + vb) / 2) or 1e-9
    return (ma - mb) / p


def main():
    dyn = load_dyn()
    settle = load_settle()
    dcount = sum(len(v) for v in dyn.values())
    print(f"dynamic events={dcount}({len(dyn)} symbol)  fast settlements(pos/neg)={len(settle)}")
    # 对齐
    aligned = []
    for r in settle:
        et = ts(r.get("source_event_time") or r.get("time"))
        de = align(dyn, r.get("symbol"), et)
        if de is None:
            continue
        r["_dyn"] = de
        r["_dir"] = 1.0 if r.get("side") == "long" else -1.0
        aligned.append(r)
    print(f"成功对齐(入场±{ALIGN_TOL}s 有 dynamic event)={len(aligned)}")
    if len(aligned) < 20:
        print("对齐样本不足(<20)。WS 采集器需先跑一段、且新 fast 仓入场时有邻近 dynamic event。稍后再跑。")
        return
    pos = [r for r in aligned if r.get("reason") in POS]
    neg = [r for r in aligned if r.get("reason") in NEG]
    print(f"对齐后 positive={len(pos)} negative={len(neg)} 基线pos率={len(pos)/len(aligned)*100:.0f}%")

    def val(r, f):
        v = r["_dyn"].get(f, 0.0) or 0.0
        # 方向相关字段朝持仓方向(trade_imbalance/book_imbalance/mid_ret:与 side 同向为正)
        if f.startswith("trade_imbalance") or f.startswith("book_imbalance") or f.startswith("mid_ret"):
            return v * r["_dir"]
        return v

    print("\n【动态字段 pos vs neg(方向相关已按 side 调向)|Cohen d| 排序】")
    res = []
    for f in NUM_FIELDS:
        pa = [val(r, f) for r in pos]; na = [val(r, f) for r in neg]
        d = cohen(pa, na)
        res.append((abs(d), f, sum(pa) / len(pa) if pa else 0, sum(na) / len(na) if na else 0, d))
    for ad, f, pm, nm, d in sorted(res, reverse=True):
        flag = "  ★有区分" if ad >= 0.2 else ("  (弱)" if ad >= 0.1 else "")
        print(f"    {f:<26} pos均={pm:+.4f} neg均={nm:+.4f} d={d:+.3f}{flag}")

    print("\n【单维 gross/net PF 子集扫描(按中位数二分;找 n>=100 & grossPF>1.3 & netPF>1.05)】")
    hits = []
    for f in NUM_FIELDS:
        vals = sorted(val(r, f) for r in aligned)
        med = vals[len(vals) // 2]
        for lbl, cond in [(f + ">med", lambda r, f=f, m=med: val(r, f) > m), (f + "<=med", lambda r, f=f, m=med: val(r, f) <= m)]:
            sub = [r for r in aligned if cond(r)]
            if len(sub) < 30:
                continue
            gs = [r.get("realized_usdt", 0) + r.get("fee_usdt", 0) for r in sub]
            ns = [r.get("realized_usdt", 0) for r in sub]
            gpf, npf = pf(gs), pf(ns)
            mark = "  ★达标" if (len(sub) >= 100 and gpf > 1.3 and npf > 1.05) else ""
            if gpf > 1.0 or mark:
                print(f"    {lbl:<28} n={len(sub):<4} grossPF={gpf:.2f} netPF={npf:.2f} gross={sum(gs):+.3f} net={sum(ns):+.3f}{mark}")
            if mark:
                hits.append(lbl)
    print("\n结论:" + ("找到达标子集 → 可作为入场候选深挖:" + ",".join(hits) if hits else "暂无 n>=100 & grossPF>1.3 & netPF>1.05 子集(样本可能不足或仍无 edge,继续采集)"))


if __name__ == "__main__":
    main()
