#!/usr/bin/env python3
# 线三 Venue Execution Target Search(只读,不接实盘)。
# 不预设 OKX 做执行端;测所有 6 个 lead->lag 方向,找哪个所更慢(更适合当执行端)。
# 信号:lead 已动(|mid_ret_3s|>=阈值)& lag 未动 → lag 后续 signed move(顺 lead 方向)。
import json, os, bisect, collections
from datetime import datetime, timezone

PATH = "crossvenue/fast_peer_leadlag_events.jsonl"
ALIGN = 1.5
LEAD_MOVE_BP = 3.0
LAG_STILL_BP = 1.0
HZ = [5, 10, 30]
PAIRS = [("binance", "okx"), ("bybit", "okx"), ("okx", "binance"),
         ("okx", "bybit"), ("binance", "bybit"), ("bybit", "binance")]
# 目标所往返成本估算(bps):maker入场+taker出场 published 近似
COST = {"okx": 5.0, "binance": 5.0, "bybit": 5.5}


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc).timestamp()
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()
    except ValueError:
        return None


def sgn(x):
    return 1 if (x or 0) > 0 else (-1 if (x or 0) < 0 else 0)


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def load():
    rows = []
    for l in open(PATH):
        l = l.strip()
        if not l:
            continue
        try:
            d = json.loads(l)
        except json.JSONDecodeError:
            continue
        t = ts(d.get("recorded_at"))
        if t and d.get("mid", 0) > 0:
            d["_t"] = t
            rows.append(d)
    return rows


def build_idx(rows, venue):
    by = collections.defaultdict(list)
    for e in rows:
        if e["venue"] == venue:
            by[e["symbol"]].append(e)
    idx = {}
    for s, v in by.items():
        v.sort(key=lambda x: x["_t"])
        idx[s] = {"ts": [e["_t"] for e in v], "mid": [e["mid"] for e in v], "mr3": [e.get("mid_ret_3s", 0) for e in v]}
    return idx


def main():
    rows = load()
    T0 = min(r["_t"] for r in rows); T1 = max(r["_t"] for r in rows)
    idx = {v: build_idx(rows, v) for v in ("okx", "binance", "bybit")}
    lead_rows = collections.defaultdict(list)
    for e in rows:
        lead_rows[e["venue"]].append(e)

    def now_i(v, s, t):
        o = idx[v].get(s)
        if not o:
            return None
        a = o["ts"]; i = bisect.bisect_left(a, t); best = None
        for j in (i - 1, i):
            if 0 <= j < len(a) and abs(a[j] - t) <= ALIGN and (best is None or abs(a[j] - t) < abs(a[best] - t)):
                best = j
        return best

    def fwd(v, s, i0, h):
        o = idx[v][s]; a = o["ts"]; m = o["mid"]; tgt = a[i0] + h; gap = max(2.0, h * 0.5)
        j = bisect.bisect_left(a, tgt)
        if j >= len(a) or a[j] > tgt + gap or m[i0] <= 0:
            return None
        return (m[j] - m[i0]) / m[i0]

    def win3(t):
        return min(2, int((t - T0) / (T1 - T0 + 1e-9) * 3)) if T1 > T0 else 0

    print(f"事件={len(rows)}  venue={{k:len(v) for...}}  信号:|lead 3s move|>={LEAD_MOVE_BP}bp & |lag 3s move|<={LAG_STILL_BP}bp(lag-follow)")
    print("每 pair:各 horizon  n/mean bps/gPF/net@cost  (cost=目标所往返估算)\n")
    summary = []
    for L, T in PAIRS:
        cost = COST[T]
        print(f"== {L} -> {T}  (目标所 {T} 成本≈{cost:.1f}bp) ==")
        best = None
        for h in HZ:
            bps = []; meta = []
            for e in lead_rows[L]:
                mv = (e.get("mid_ret_3s", 0) or 0) * 1e4
                if abs(mv) < LEAD_MOVE_BP:
                    continue
                s = e["symbol"]; i0 = now_i(T, s, e["_t"])
                if i0 is None or abs(idx[T][s]["mr3"][i0] * 1e4) > LAG_STILL_BP:
                    continue
                r = fwd(T, s, i0, h)
                if r is None:
                    continue
                bps.append(r * sgn(mv) * 1e4); meta.append((s, win3(e["_t"])))
            if not bps:
                print(f"    {h}s: —"); continue
            n = len(bps); mean = sum(bps) / n; g = pf(bps); nc = pf([x - cost for x in bps]); pos = sum(1 for x in bps if x > 0) / n * 100
            bysy = collections.Counter(s for s, _ in meta); byw = collections.defaultdict(list)
            for b, (s, w) in zip(bps, meta):
                byw[w].append(b)
            top = max(bysy.values()) / n * 100; wpos = sum(1 for x in byw.values() if sum(x) > 0)
            print(f"    {h}s: n={n} mean={mean:+.2f}bp gPF={g:.2f} net@{cost:.0f}={nc:.2f} pos={pos:.0f}% 跨symbol={len(bysy)} 最大占比={top:.0f}% 正窗口={wpos}/3")
            if best is None or mean > best[1]:
                best = (h, mean, g, nc, n, len(bysy), top, wpos)
        if best:
            summary.append((L, T, best))
        print()

    print("=" * 66)
    print("【汇总:各 pair 最佳 horizon 的 lag 位移(mean bps 降序)】")
    for L, T, (h, mean, g, nc, n, nsym, top, wpos) in sorted(summary, key=lambda x: -x[2][1]):
        robust = "" if (nsym >= 5 and top < 40 and wpos >= 2) else "  (单symbol/窗口存疑)"
        print(f"  {L:>8} -> {T:<8} +{h}s: mean={mean:+.2f}bp gPF={g:.2f} net@cost={nc:.2f} n={n} 跨symbol={nsym}{robust}")

    # 结论
    tgt = collections.defaultdict(list); src = collections.defaultdict(list)
    for L, T, b in summary:
        tgt[T].append(b[1]); src[L].append(b[1])
    print("\n【结论】")
    if tgt:
        bt = max(tgt, key=lambda v: max(tgt[v]))
        print(f"  最适合当【执行端(lag,滞后最大)】:{bt}(最佳 lag 位移 {max(tgt[bt]):+.2f}bp)")
    if src:
        bs = max(src, key=lambda v: max(src[v]))
        print(f"  最适合当【lead 源】:{bs}(带动最大 {max(src[bs]):+.2f}bp)")
    passed = [(L, T, b) for L, T, b in summary if b[1] >= 5 and b[2] > 1.3 and b[3] > 1.05 and b[4] >= 100 and b[5] >= 5 and b[6] < 40 and b[7] >= 2]
    if passed:
        print("  ★ 存在 >=5bps 且过成本的 pair:")
        for L, T, b in passed:
            print(f"      {L}->{T} +{b[0]}s mean={b[1]:+.2f}bp net@cost={b[3]:.2f}")
    else:
        print("  ✗ 无任何 pair 达到 >=5bps 可预测位移且过成本 → 换执行所本身不能解决位移不足。")


if __name__ == "__main__":
    main()
