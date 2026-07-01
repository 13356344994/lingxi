#!/usr/bin/env python3
# 线一 Inefficient Universe Search(只读,不接实盘)。
# 目标:在扩大后的 universe 里找「更低效率、滞后更大」的币(lag-follow 弹性)。
# 信号:peer(Binance/Bybit)已动 & OKX 尚未动 → OKX 后续 signed move(顺 peer 方向)。
# 每 symbol 输出 n / mean bps / grossPF / net@5 / pos rate,并过滤可交易性(spread 不极端 & 样本够)。
import json, os, bisect, collections
from datetime import datetime, timezone

PATH = "crossvenue/fast_peer_leadlag_events.jsonl"
ALIGN = 1.5
PEER_MOVE_BP = 3.0     # peer 已动阈值
OKX_STILL_BP = 1.0     # OKX 尚未动阈值
MAX_SPREAD_BP = 20.0   # OKX 中位 spread 上限(过滤极端不可交易)
MIN_N = 100
HZ = [5, 10, 30, 60]


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


def main():
    rows = load()
    okx = [r for r in rows if r.get("venue") == "okx"]
    peer = [r for r in rows if r.get("venue") in ("binance", "bybit")]
    if not okx or not peer:
        print("数据不足。"); return
    T0 = min(r["_t"] for r in okx); T1 = max(r["_t"] for r in okx)
    # OKX 索引
    O = {}
    for e in okx:
        O.setdefault(e["symbol"], []).append(e)
    for s in O:
        O[s].sort(key=lambda x: x["_t"])
    idx = {s: {"ts": [e["_t"] for e in v], "mid": [e["mid"] for e in v],
               "spread": [e.get("spread_bps", 0) for e in v], "mr3": [e.get("mid_ret_3s", 0) for e in v]} for s, v in O.items()}

    def now_i(s, t):
        o = idx.get(s)
        if not o:
            return None
        a = o["ts"]; i = bisect.bisect_left(a, t); best = None
        for j in (i - 1, i):
            if 0 <= j < len(a) and abs(a[j] - t) <= ALIGN and (best is None or abs(a[j] - t) < abs(a[best] - t)):
                best = j
        return best

    def fwd(s, i0, h):
        o = idx[s]; a = o["ts"]; m = o["mid"]; tgt = a[i0] + h; gap = max(2.0, h * 0.5)
        j = bisect.bisect_left(a, tgt)
        if j >= len(a) or a[j] > tgt + gap:
            return None
        return (m[j] - m[i0]) / m[i0] if m[i0] > 0 else None

    def win3(t):
        return min(2, int((t - T0) / (T1 - T0 + 1e-9) * 3)) if T1 > T0 else 0

    # 收集 lag-follow 样本:peer 已动 & OKX 未动
    persym = collections.defaultdict(lambda: {h: [] for h in HZ})  # sym -> h -> [(bps, win)]
    persym[""]  # noop
    samp = collections.defaultdict(lambda: collections.defaultdict(list))
    for e in peer:
        mv = (e.get("mid_ret_3s", 0) or 0) * 1e4
        if abs(mv) < PEER_MOVE_BP:
            continue
        s = e["symbol"]; i0 = now_i(s, e["_t"])
        if i0 is None:
            continue
        if abs(idx[s]["mr3"][i0] * 1e4) > OKX_STILL_BP:  # OKX 已经动了→不算 lag
            continue
        side = sgn(mv); w = win3(e["_t"])
        for h in HZ:
            r = fwd(s, i0, h)
            if r is not None:
                samp[s][h].append((r * side * 1e4, w))

    # 每 symbol 输出(以 30s 为主展示,candidate 看全 horizon)
    medspread = {s: sorted(idx[s]["spread"])[len(idx[s]["spread"]) // 2] for s in idx if idx[s]["spread"]}
    print(f"OKX universe symbols={len(idx)}  peer lag-follow 命中 symbols={len(samp)}")
    print(f"过滤:OKX中位spread<={MAX_SPREAD_BP}bp & n>={MIN_N}  (信号:|peer 3s move|>={PEER_MOVE_BP}bp & |OKX 3s move|<={OKX_STILL_BP}bp)")
    print("\n【每 symbol lag-follow(按 30s mean 排序;n 为该 horizon 样本)】")
    print(f"  {'symbol':<14}{'spr':>6} | " + "  ".join(f"{h}s:n/mean/gPF/n5" for h in HZ))
    rowsout = []
    for s, hm in samp.items():
        sp = medspread.get(s, 999)
        n30 = len(hm[30])
        mean30 = sum(b for b, _ in hm[30]) / n30 if n30 else -999
        rowsout.append((s, sp, mean30, hm))
    cands = []
    for s, sp, mean30, hm in sorted(rowsout, key=lambda x: -x[2]):
        cells = []
        for h in HZ:
            v = hm[h]
            if not v:
                cells.append(f"{h}s:—"); continue
            bps = [b for b, _ in v]; n = len(bps); mean = sum(bps) / n
            g = pf(bps); n5 = pf([x - 5 for x in bps]); pos = sum(1 for x in bps if x > 0) / n * 100
            cells.append(f"{h}s:{n}/{mean:+.1f}/{g:.2f}/{n5:.2f}")
            if sp <= MAX_SPREAD_BP and n >= MIN_N and mean >= 5 and g > 1.3 and n5 > 1.05:
                # 单窗口检验
                byw = collections.defaultdict(list)
                for b, w in v:
                    byw[w].append(b)
                wpos = sum(1 for x in byw.values() if sum(x) > 0)
                cands.append((s, h, n, mean, g, n5, pos, sp, wpos))
        flag = "" if sp <= MAX_SPREAD_BP else "  [spread极端]"
        print(f"  {s:<14}{sp:>6.1f} | " + "  ".join(cells) + flag)

    print("\n【universe 候选池(n>=100 & mean>=5bp & gPF>1.3 & net@5>1.05 & spread不极端 & 跨窗口)】")
    real = [c for c in cands if c[8] >= 2]
    if not real:
        print("  无 symbol 达标(lag-follow 位移仍不足以过成本)。")
        if cands:
            print("  (有统计达标但单窗口假阳:" + ", ".join(f"{c[0]}+{c[1]}s" for c in cands) + ")")
    for s, h, n, mean, g, n5, pos, sp, wpos in real:
        print(f"  ★ {s} +{h}s: n={n} mean={mean:+.1f}bp gPF={g:.2f} net@5={n5:.2f} pos={pos:.0f}% spr={sp:.1f}bp 正窗口={wpos}/3")
    print("\n注:新扩入的非主流币样本仍在累积,n 不足者需更长采集窗口再复核。候选池仅指导 universe 选择,不做黑名单、不直接上线。")


if __name__ == "__main__":
    main()
