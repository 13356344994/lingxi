#!/usr/bin/env python3
# cross-venue lead-lag「位移放大来源」研究(只读,不接实盘,不改任何策略)。
# 目标:不是再证明 1bps 信号存在,而是搜索是否存在能产生 >=5bps 可预测位移的过成本子集。
# 数据:records/crossvenue/fast_peer_leadlag_events.jsonl(venue=okx/binance/bybit,单文件跨所对齐)。
# 门槛:n>=100 & mean signed >=5bps & grossPF>1.3 & netPF@5>1.05 & 不依赖单symbol/单窗口。
import json, os, bisect, collections
from datetime import datetime, timezone

PATH = "crossvenue/fast_peer_leadlag_events.jsonl"
HZ = [3, 5, 10, 15, 30, 60]          # forward horizon(秒)——含更长尺度
ALIGN = 1.5                          # peer 事件对齐 OKX "当前" 的容差秒
CANDS = []                           # 全局候选收集


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
    if not os.path.exists(PATH):
        return rows
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


# ---------- OKX 索引 ----------
OKX = {}   # sym -> dict(ts, mid, spread, mr3)
T0 = T1 = 0.0


def build_okx(okx_rows):
    global T0, T1
    by = collections.defaultdict(list)
    for e in okx_rows:
        by[e["symbol"]].append(e)
    for sym, evs in by.items():
        evs.sort(key=lambda x: x["_t"])
        OKX[sym] = {
            "ts": [e["_t"] for e in evs],
            "mid": [e["mid"] for e in evs],
            "spread": [e.get("spread_bps", 0) for e in evs],
            "mr3": [e.get("mid_ret_3s", 0) for e in evs],
        }
    allt = [e["_t"] for e in okx_rows]
    T0, T1 = min(allt), max(allt)


def okx_now(sym, t):
    o = OKX.get(sym)
    if not o:
        return None
    arr = o["ts"]; i = bisect.bisect_left(arr, t); best = None
    for j in (i - 1, i):
        if 0 <= j < len(arr) and abs(arr[j] - t) <= ALIGN and (best is None or abs(arr[j] - t) < abs(arr[best] - t)):
            best = j
    return best


def okx_fwd(sym, i0, h):
    o = OKX[sym]; arr = o["ts"]; mids = o["mid"]
    target = arr[i0] + h; gap = max(2.0, h * 0.5)
    j = bisect.bisect_left(arr, target)
    if j >= len(arr) or arr[j] > target + gap:
        return None
    m0 = mids[i0]
    return (mids[j] - m0) / m0 if m0 > 0 else None


def window3(t):
    # 把时间轴切成三段做「单窗口假阳」鲁棒性检验(数据多在同一小时,故用等分窗口)
    if T1 <= T0:
        return 0
    return min(2, int((t - T0) / (T1 - T0 + 1e-9) * 3))


# ---------- 通用评估 ----------
# samples: list of (side, sym, t, i0)
def evaluate(name, samples, horizons=HZ, star_hz=(10, 30), quiet=False):
    if not samples:
        if not quiet:
            print(f"  {name:<40} (无样本)")
        return
    cells = []
    for h in horizons:
        bps = []; meta = []
        for side, sym, t, i0 in samples:
            r = okx_fwd(sym, i0, h)
            if r is None:
                continue
            bps.append(r * side * 10000); meta.append((sym, window3(t)))
        if not bps:
            cells.append(f"{h}s:—"); continue
        n = len(bps); mean = sum(bps) / n; pos = sum(1 for x in bps if x > 0) / n * 100
        g = pf(bps); n5 = pf([x - 5 for x in bps]); n10 = pf([x - 10 for x in bps])
        cells.append(f"{h}s:n{n} {mean:+.1f}bp g{g:.2f}/n5{n5:.2f}/n10{n10:.2f} {pos:.0f}%")
        # 候选门槛
        if n >= 100 and g > 1.3 and n5 > 1.05 and mean >= 5:
            CANDS.append((name, h, n, mean, g, n5, n10, pos, bps, meta))
    if not quiet:
        print(f"  {name:<40} " + "  ".join(cells))


def peers_by_venue(peer):
    d = collections.defaultdict(list)
    for e in peer:
        d[e["venue"]].append(e)
    return d


def main():
    rows = load()
    okx = [r for r in rows if r.get("venue") == "okx"]
    peer = [r for r in rows if r.get("venue") in ("binance", "bybit")]
    print(f"总事件={len(rows)}  OKX={len(okx)}  peer={len(peer)}")
    if not okx or not peer:
        print("数据不足。"); return
    build_okx(okx)
    # 预对齐:每个 peer 事件的 OKX 当前 idx
    for e in peer:
        e["_i0"] = okx_now(e["symbol"], e["_t"])
    peer = [e for e in peer if e["_i0"] is not None]
    pv = peers_by_venue(peer)
    print(f"对齐后 peer={len(peer)}  binance={len(pv.get('binance',[]))} bybit={len(pv.get('bybit',[]))}")
    print(f"时间跨度≈{(T1-T0)/60:.1f}min  (窗口鲁棒性=等分三段)")

    def mv(e, k):  # peer 已发生的 move(bps)
        return (e.get(k, 0) or 0) * 10000

    # ===== 1. 时间尺度放大(固定信号=peer 3s move 顺势,看长 horizon 是否释放更多位移)=====
    print("\n【1. 时间尺度放大 — peer mid_ret_3s 顺势 → OKX forward(3..60s)】")
    for v in ("binance", "bybit"):
        s = [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in pv.get(v, []) if sgn(mv(e, "mid_ret_3s")) != 0]
        evaluate(f"{v} 3s顺势", s)

    # ===== 2. 波动/活跃度过滤(只看 peer 已经动起来的时刻)=====
    print("\n【2. 波动过滤 — 仅 peer 3s/5s move 已超阈值(顺势),看 OKX 是否放大到 >=5bps】")
    for v in ("binance", "bybit"):
        for key, th in [("mid_ret_3s", 3), ("mid_ret_3s", 5), ("mid_ret_3s", 8), ("mid_ret_5s", 5), ("mid_ret_5s", 8)]:
            s = [(sgn(mv(e, key)), e["symbol"], e["_t"], e["_i0"]) for e in pv.get(v, []) if abs(mv(e, key)) >= th]
            evaluate(f"{v} |{key}|>={th}bp 顺势", s)
    # 高活跃度(trade_count,binance 无成交流→用 bybit)
    print("  -- 高活跃度过滤(bybit trade_count_5s 高分位)--")
    byb = pv.get("bybit", [])
    tcs = sorted(e.get("trade_count_5s", 0) for e in byb)
    if tcs:
        q80 = tcs[int(len(tcs) * 0.8)]
        s = [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in byb if e.get("trade_count_5s", 0) >= q80 and sgn(mv(e, "mid_ret_3s")) != 0]
        evaluate(f"bybit tc5s>={q80}(top20%) 3s顺势", s)

    # ===== 3. symbol 分层(信号=bybit 3s 顺势;horizon 10/30)=====
    print("\n【3. symbol 分层 — bybit 3s顺势,各 symbol 弹性(horizon=10s)】")
    persym = collections.defaultdict(list)
    for e in byb:
        sd = sgn(mv(e, "mid_ret_3s"))
        if sd != 0:
            persym[e["symbol"]].append((sd, e["symbol"], e["_t"], e["_i0"]))
    rowsout = []
    for sym, s in persym.items():
        bps = []
        for side, sy, t, i0 in s:
            r = okx_fwd(sy, i0, 10)
            if r is not None:
                bps.append(r * side * 10000)
        if len(bps) >= 50:
            rowsout.append((sym, len(bps), sum(bps) / len(bps), pf(bps), sum(1 for x in bps if x > 0) / len(bps) * 100))
    for sym, n, mean, g, pos in sorted(rowsout, key=lambda x: -x[2]):
        print(f"    {sym:<12} n={n:<5} mean={mean:+.2f}bp PF={g:.2f} pos={pos:.0f}%")

    # ===== 4. venue 领先源分层 + 共振 =====
    print("\n【4. venue 领先源 — Binance单独 / Bybit单独 / 两所同向共振 → OKX forward】")
    evaluate("binance 3s顺势(单独)", [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in pv.get("binance", []) if sgn(mv(e, "mid_ret_3s")) != 0])
    evaluate("bybit 3s顺势(单独)", [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in byb if sgn(mv(e, "mid_ret_3s")) != 0])
    # 共振:同 symbol,binance 事件时刻附近找最近 bybit 事件(<=1s),两者 3s move 同向
    byb_idx = collections.defaultdict(lambda: ([], []))  # sym -> (ts[], side3[])
    for e in sorted(byb, key=lambda x: x["_t"]):
        byb_idx[e["symbol"]][0].append(e["_t"]); byb_idx[e["symbol"]][1].append(sgn(mv(e, "mid_ret_3s")))
    reson = []
    for e in pv.get("binance", []):
        sb = sgn(mv(e, "mid_ret_3s"))
        if sb == 0:
            continue
        arr, sides = byb_idx.get(e["symbol"], ([], []))
        if not arr:
            continue
        j = bisect.bisect_left(arr, e["_t"])
        best = None
        for k in (j - 1, j):
            if 0 <= k < len(arr) and abs(arr[k] - e["_t"]) <= 1.0 and (best is None or abs(arr[k] - e["_t"]) < abs(arr[best] - e["_t"])):
                best = k
        if best is not None and sides[best] == sb:
            reson.append((sb, e["symbol"], e["_t"], e["_i0"]))
    evaluate("binance+bybit 同向共振", reson)

    # ===== 5. spread/成本过滤(仅 OKX spread 窄时,看净值是否接近过成本)=====
    print("\n【5. 成本过滤 — 仅 OKX spread 窄时(bybit 3s顺势)】")
    for sp in (1.0, 2.0, 3.0):
        s = []
        for e in byb:
            sd = sgn(mv(e, "mid_ret_3s"))
            if sd == 0:
                continue
            i0 = e["_i0"]; osp = OKX[e["symbol"]]["spread"][i0]
            if osp <= sp:
                s.append((sd, e["symbol"], e["_t"], i0))
        evaluate(f"bybit 3s顺势 & OKX_spread<={sp}bp", s)

    # ===== 6. 方向类型(分开测,不混)=====
    print("\n【6. 方向类型 — 分别测试(bybit;horizon 5/10/30)】")
    HZ6 = [5, 10, 30]
    cont = [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in byb if sgn(mv(e, "mid_ret_3s")) != 0]
    rev = [(-sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in byb if sgn(mv(e, "mid_ret_3s")) != 0]
    conv = [(sgn(e["mid"] - OKX[e["symbol"]]["mid"][e["_i0"]]), e["symbol"], e["_t"], e["_i0"]) for e in byb if sgn(e["mid"] - OKX[e["symbol"]]["mid"][e["_i0"]]) != 0]
    lag = [(sgn(mv(e, "mid_ret_3s")), e["symbol"], e["_t"], e["_i0"]) for e in byb
           if abs(mv(e, "mid_ret_3s")) >= 3 and abs(OKX[e["symbol"]]["mr3"][e["_i0"]] * 10000) <= 1]
    evaluate("peer impulse continuation", cont, HZ6)
    evaluate("peer impulse reversal", rev, HZ6)
    evaluate("cross-venue spread convergence", conv, HZ6)
    evaluate("OKX lag-follow(peer动OKX未动)", lag, HZ6)

    # ===== 裁决 =====
    print("\n" + "=" * 70)
    print("【裁决:达标子集(n>=100 & mean>=5bp & grossPF>1.3 & netPF@5>1.05 & 不依赖单symbol/单窗口)】")
    if not CANDS:
        print("  无任何统计达标子集。")
    real = []
    for name, h, n, mean, g, n5, n10, pos, bps, meta in CANDS:
        bysy = collections.defaultdict(list); byw = collections.defaultdict(list)
        for b, (sy, w) in zip(bps, meta):
            bysy[sy].append(b); byw[w].append(b)
        top = max((len(v) for v in bysy.values()), default=0) / max(1, n) * 100
        wpos = sum(1 for v in byw.values() if sum(v) > 0)
        robust = top < 40 and len(bysy) >= 5 and wpos >= 2
        tag = "★真候选" if robust else "△假阳(单symbol/单窗口)"
        real.append(robust)
        print(f"  {tag} {name} +{h}s: n={n} mean={mean:+.1f}bp gPF={g:.2f} n5={n5:.2f} n10={n10:.2f} pos={pos:.0f}% 跨symbol={len(bysy)} 最大占比={top:.0f}% 正窗口={wpos}/3")

    print("\n【结论】")
    if any(real):
        print("  存在 ★真候选(见上)。可据此指导 universe / horizon 选择,但上线前仍需更多样本复核。")
    else:
        print("  当前 OKX fast universe 的可预测位移不足以支持 5-10bps 成本下的高频实盘,")
        print("  下一步必须换 universe / 换成本结构 / 换交易所 / 换执行方式。")


if __name__ == "__main__":
    main()
