#!/usr/bin/env python3
# cross-venue lead-lag 复盘(只读,不下单)。
# 测试 Binance/Bybit(peer)是否领先 OKX:peer 当前信号 → 预测 OKX 后续 1/3/5/10s mid 位移。
# 门槛:n>=100 & mean signed >=5bps & grossPF>1.3 & net@5>1.05 & 不依赖单symbol/单窗口。
import json, os, bisect, collections
from datetime import datetime, timezone

PEER = "crossvenue/fast_peer_leadlag_events.jsonl"  # 含 okx/binance/bybit 三 venue,单文件对齐
HZ = [1, 3, 5, 10]
ALIGN = 1.5  # peer 事件对齐 OKX "当前" mid 的容差秒


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc).timestamp()
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()
    except ValueError:
        return None


def load(path, extra=False):
    rows = []
    if not os.path.exists(path):
        return rows
    for l in open(path):
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
            d["_hour"] = datetime.fromtimestamp(t, tz=timezone.utc).hour
            rows.append(d)
    return rows


def sgn(x):
    return 1 if (x or 0) > 0 else (-1 if (x or 0) < 0 else 0)


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def main():
    allrows = load(PEER)
    okx = [r for r in allrows if r.get("venue") == "okx"]
    peer = [r for r in allrows if r.get("venue") in ("binance", "bybit")]
    print(f"总事件={len(allrows)}  OKX={len(okx)}  peer(binance/bybit)={len(peer)}")
    if not peer or not okx:
        print("数据不足(需 peer 采集器写入 okx+binance/bybit 三 venue)。稍后再跑。")
        return
    # OKX 索引:symbol -> (ts列表, mid列表)
    okx_ts = collections.defaultdict(list); okx_mid = collections.defaultdict(list)
    for e in sorted(okx, key=lambda x: x["_t"]):
        okx_ts[e["symbol"]].append(e["_t"]); okx_mid[e["symbol"]].append(e["mid"])

    def okx_now(sym, t):
        arr = okx_ts.get(sym)
        if not arr:
            return None
        i = bisect.bisect_left(arr, t)
        best = None
        for j in (i - 1, i):
            if 0 <= j < len(arr) and abs(arr[j] - t) <= ALIGN and (best is None or abs(arr[j] - t) < abs(arr[best] - t)):
                best = j
        return best

    def okx_fwd(sym, i0, h):
        arr = okx_ts.get(sym); mids = okx_mid.get(sym)
        target = arr[i0] + h; gap = h * 0.6 + 2
        j = bisect.bisect_left(arr, target)
        if j >= len(arr) or arr[j] > target + gap:
            return None
        m0 = mids[i0]
        return (mids[j] - m0) / m0 if m0 > 0 else None

    peer_by_v = collections.defaultdict(list)
    for e in peer:
        peer_by_v[e["venue"]].append(e)

    def sig_side(e, name):
        if name == "midret_1s":
            return sgn(e.get("mid_ret_1s"))
        if name == "midret_3s":
            return sgn(e.get("mid_ret_3s"))
        if name == "tradeimb_1s":
            return sgn((e.get("trade_buy_qty_1s", 0) or 0) - (e.get("trade_sell_qty_1s", 0) or 0))
        if name == "tradeimb_3s":
            return sgn((e.get("trade_buy_qty_3s", 0) or 0) - (e.get("trade_sell_qty_3s", 0) or 0))
        if name == "bookimb":
            return sgn(e.get("book_imbalance"))
        return 0

    SIGNALS = ["midret_1s", "midret_3s", "tradeimb_1s", "tradeimb_3s", "bookimb", "xspread"]
    print("\n【peer 信号 → OKX 后续位移(signed bps;gross/net@5/net@10;重点 +3/5s)】")
    cands = []
    for venue in sorted(peer_by_v):
        evs = peer_by_v[venue]
        print(f"\n== venue={venue} (peer events={len(evs)}) ==")
        for name in SIGNALS:
            row = []
            for h in [3, 5, 10]:
                bps = []; meta = []
                for e in evs:
                    i0 = okx_now(e["symbol"], e["_t"])
                    if i0 is None:
                        continue
                    if name == "xspread":
                        okxm = okx_mid[e["symbol"]][i0]
                        sd = sgn(e["mid"] - okxm)  # peer 比 OKX 贵→OKX 跟涨(多)
                    else:
                        sd = sig_side(e, name)
                    if sd == 0:
                        continue
                    r = okx_fwd(e["symbol"], i0, h)
                    if r is None:
                        continue
                    bps.append(r * sd * 10000); meta.append((e["symbol"], e["_hour"]))
                if not bps:
                    row.append(f"+{h}s:—"); continue
                n = len(bps); mean = sum(bps) / n; pos = sum(1 for x in bps if x > 0) / n * 100
                g = pf(bps); n5 = pf([x - 5 for x in bps]); n10 = pf([x - 10 for x in bps])
                row.append(f"+{h}s:n{n} g{g:.2f}/n5{n5:.2f} {mean:+.1f}bps {pos:.0f}%")
                if n >= 100 and g > 1.3 and n5 > 1.05:
                    cands.append((venue, name, h, n, g, n5, n10, mean, pos, bps, meta))
            print(f"    {name:<14} " + "  ".join(row))

    print("\n【筛选(n>=100 & grossPF>1.3 & net@5>1.05 & mean>=5bps & 不依赖单symbol)】")
    if not cands:
        print("  cross-venue lead-lag 暂未发现过成本 edge。")
    for venue, name, h, n, g, n5, n10, mean, pos, bps, meta in cands:
        bysy = collections.defaultdict(list); byhr = collections.defaultdict(list)
        for b, (sy, hr) in zip(bps, meta):
            bysy[sy].append(b); byhr[hr].append(b)
        top = max((len(v) for v in bysy.values()), default=0) / max(1, n) * 100
        nhrpos = sum(1 for v in byhr.values() if sum(v) > 0)
        ok = mean >= 5 and top < 40 and len(bysy) >= 5 and nhrpos >= 3
        print(f"  {'★达标' if ok else '△统计达标但疑假阳'} {venue}/{name}+{h}s: n={n} gPF={g:.2f} n5={n5:.2f} n10={n10:.2f} mean={mean:+.1f}bps pos={pos:.0f}% 跨symbol={len(bysy)} 最大占比={top:.0f}% 跨hour正={nhrpos}")

    print("\n结论:" + ("见上候选" if cands else "cross-venue lead-lag 暂未发现过成本 edge(mean<5bps 或 net@5<1.05)"))


if __name__ == "__main__":
    main()
