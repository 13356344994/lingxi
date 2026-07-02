#!/usr/bin/env python3
# 方向×波动爆发 组合 forward-return 研究(只读,不下单)。
# 只用 fast_dynamic_feature_events;找能产生 >=5bps 可交易位移的高频场景(单信号已证太小)。
import json, os, bisect, collections
from datetime import datetime, timezone

DYN = "okx_directional/fast_dynamic_feature_events.jsonl"
HZ = [1, 3, 5, 10, 30]


def load():
    ev = []
    if not os.path.exists(DYN):
        return ev
    for l in open(DYN):
        l = l.strip()
        if not l:
            continue
        try:
            d = json.loads(l)
        except json.JSONDecodeError:
            continue
        s = (d.get("recorded_at") or "").rstrip("Z")
        try:
            if "." in s:
                h, f = s.split(".", 1)
                dt = datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f")
            else:
                dt = datetime.strptime(s, "%Y-%m-%dT%H:%M:%S")
        except ValueError:
            continue
        d["_ts"] = dt.replace(tzinfo=timezone.utc).timestamp()
        d["_hour"] = dt.hour
        if d.get("mid", 0) > 0:
            ev.append(d)
    return ev


def sgn(x):
    return 1 if (x or 0) > 0 else (-1 if (x or 0) < 0 else 0)


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def quantile(vals, q):
    if not vals:
        return 0
    v = sorted(vals)
    return v[min(len(v) - 1, int(q * len(v)))]


def main():
    ev = load()
    bysym = collections.defaultdict(list)
    for e in ev:
        bysym[e["symbol"]].append(e)
    for s in bysym:
        bysym[s].sort(key=lambda x: x["_ts"])
    keys = {s: [e["_ts"] for e in arr] for s, arr in bysym.items()}
    # 每条事件预存所在 symbol 的 index
    idx = {}
    for s, arr in bysym.items():
        for i, e in enumerate(arr):
            idx[id(e)] = (s, i)

    def fwd(e, h):
        s, i = idx[id(e)]
        arr = bysym[s]; k = keys[s]
        t0 = e["_ts"]; m0 = e["mid"]; target = t0 + h; gap = h * 0.6 + 2
        j = bisect.bisect_left(k, target)
        if j >= len(arr) or k[j] > target + gap:
            return None
        return (arr[j]["mid"] - m0) / m0

    # 阈值(分位)
    tc3 = quantile([e.get("trade_count_3s", 0) for e in ev], 0.70)
    ltn = quantile([e.get("large_trade_notional", 0) for e in ev if e.get("large_trade_notional", 0) > 0], 0.50)
    spr70 = quantile([e.get("spread_bps", 0) for e in ev], 0.60)
    print(f"dynamic n={len(ev)} symbol={len(bysym)} | 阈值 trade_count_3s_q70={tc3} large_notional_q50={ltn:.0f} spread_q60={spr70:.1f}bps")

    # 组合定义: (name, filterfn, sidefn)
    combos = [
        ("1a.tradeCount爆发+imb3s同向", lambda e: e.get("trade_count_3s", 0) >= tc3 and sgn(e.get("trade_imbalance_3s")) != 0, lambda e: sgn(e.get("trade_imbalance_3s"))),
        ("1b.large_trade>0+imb5s同向", lambda e: (e.get("large_trade_count", 0) or 0) > 0 and sgn(e.get("trade_imbalance_5s")) != 0, lambda e: sgn(e.get("trade_imbalance_5s"))),
        ("1c.large_notional高+imb3s同向", lambda e: (e.get("large_trade_notional", 0) or 0) >= ltn and sgn(e.get("trade_imbalance_3s")) != 0, lambda e: sgn(e.get("trade_imbalance_3s"))),
        ("2.bookImb强+delta同向+tc爆发", lambda e: abs(e.get("book_imbalance_now", 0)) >= 0.3 and sgn(e.get("book_imbalance_delta_3s")) == sgn(e.get("book_imbalance_now")) and e.get("trade_count_3s", 0) >= tc3, lambda e: sgn(e.get("book_imbalance_now"))),
        ("3a.pull_bid+midRet3s<=0→空", lambda e: (e.get("liquidity_pull_bid", 0) or 0) > 0 and (e.get("mid_ret_3s", 0) or 0) <= 0, lambda e: -1),
        ("3b.pull_ask+midRet3s>=0→多", lambda e: (e.get("liquidity_pull_ask", 0) or 0) > 0 and (e.get("mid_ret_3s", 0) or 0) >= 0, lambda e: 1),
        ("3c.refill_bid+midRet1s>=0→多", lambda e: (e.get("liquidity_refill_bid", 0) or 0) > 0 and (e.get("mid_ret_1s", 0) or 0) >= 0, lambda e: 1),
        ("3d.refill_ask+midRet1s<=0→空", lambda e: (e.get("liquidity_refill_ask", 0) or 0) > 0 and (e.get("mid_ret_1s", 0) or 0) <= 0, lambda e: -1),
        ("4.动量midRet3s+tc爆发+spread不宽", lambda e: sgn(e.get("mid_ret_3s")) != 0 and e.get("trade_count_3s", 0) >= tc3 and e.get("spread_bps", 1e9) <= spr70, lambda e: sgn(e.get("mid_ret_3s"))),
    ]

    print("\n【组合 forward(signed bps;gross / net@5 / net@10;重点 +3/5s)】")
    cands = []
    for name, filt, side in combos:
        sub = [e for e in ev if filt(e)]
        row = []
        keep = None
        for h in [3, 5, 10]:
            bps = []
            meta = []
            for e in sub:
                sd = side(e)
                if sd == 0:
                    continue
                r = fwd(e, h)
                if r is None:
                    continue
                bps.append(r * sd * 10000)
                meta.append((idx[id(e)][0], e["_hour"], sd))
            if not bps:
                row.append(f"+{h}s:—")
                continue
            n = len(bps); mean = sum(bps) / n; pos = sum(1 for x in bps if x > 0) / n * 100
            g = pf(bps); n5 = pf([x - 5 for x in bps]); n10 = pf([x - 10 for x in bps])
            row.append(f"+{h}s:n{n} g{g:.2f}/n5{n5:.2f}/n10{n10:.2f} {mean:+.1f}bps {pos:.0f}%")
            if n >= 100 and g > 1.3 and n5 > 1.05:
                cands.append((name, h, n, g, n5, n10, mean, pos, bps, meta))
        print(f"  {name:<34} " + "   ".join(row))

    print("\n【筛选(n>=100 & grossPF>1.3 & net@5>1.05 & mean≳5bps & 不依赖单symbol)】")
    if not cands:
        print("  方向×波动爆发暂未发现过成本 edge。")
    for name, h, n, g, n5, n10, mean, pos, bps, meta in cands:
        bysy = collections.defaultdict(list); byhr = collections.defaultdict(list)
        for b, (sy, hr, sd) in zip(bps, meta):
            bysy[sy].append(b); byhr[hr].append(b)
        topshare = max((len(v) for v in bysy.values()), default=0) / max(1, n) * 100
        nsym_pos = sum(1 for v in bysy.values() if len(v) >= 10 and sum(v) > 0)
        nhr_pos = sum(1 for v in byhr.values() if sum(v) > 0)
        ok = mean >= 4 and topshare < 40 and len(bysy) >= 5 and nhr_pos >= 3
        print(f"  {'★达标' if ok else '△达统计门槛但疑假阳'} {name}+{h}s: n={n} gPF={g:.2f} n5={n5:.2f} n10={n10:.2f} mean={mean:+.1f}bps pos={pos:.0f}% | 跨symbol={len(bysy)}(正贡献{nsym_pos}) 最大占比={topshare:.0f}% 跨hour正={nhr_pos}")

    print("\n结论:" + ("找到候选,见上(★=跨symbol跨窗+均值达标)" if cands else "方向×波动爆发暂未发现过成本 edge(mean 仍 <5bps 或 net@5<1.05)"))


if __name__ == "__main__":
    main()
