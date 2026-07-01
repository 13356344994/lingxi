#!/usr/bin/env python3
# fast-layer v1 离线复核(只读 / 反事实 / 需外网)。
# #1 opposite_btc: 分 OKX/Bybit(+binance) × opened/rejected/maker_not_filled × 3m/5m/15m × 扣 5/10/15bps。
# #4 extreme_funding(env.funding_bucket==extreme): 3m/5m(+15m) 正 edge 复核(只看快前向,不用慢持仓结论)。
# 前向收益=OKX 1m K线 signed(side调整);bybit/binance 标的用 OKX 同币近似;OKX 无此币→跳过。
# 用 history-candles 翻页覆盖全事件窗(~23h)。反事实=signed 价格收益,非实盘 PnL。
import json, os, time, urllib.request
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
HZ = (3, 5, 15)
COSTS = (0.0005, 0.0010, 0.0015)


def ts(s):
    s = (s or "").rstrip("Z").split(".")[0]
    try:
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def load():
    ev = []
    for v in VEN:
        f = f"{v}_directional/shadow_fast_signal_events.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            l = l.strip()
            if l:
                try:
                    ev.append(json.loads(l))
                except json.JSONDecodeError:
                    pass
    return ev


_cache = {}
def _get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    for _ in range(4):
        try:
            d = json.load(urllib.request.urlopen(req, timeout=15))
            if d.get("code") == "0":
                return d.get("data", [])
            time.sleep(0.8)  # 限频等
        except Exception:
            time.sleep(0.8)
    return []


def candles(sym, since_ms):
    if sym in _cache:
        return _cache[sym]
    inst = sym + "-SWAP"
    out = {}
    for r in _get("https://www.okx.com/api/v5/market/candles?instId=%s&bar=1m&limit=300" % inst):
        out[int(r[0])] = float(r[4])
    for _ in range(28):  # 翻页覆盖更早(每页100分钟)
        if out and min(out) <= since_ms:
            break
        after = min(out) if out else int(time.time() * 1000)
        rows = _get("https://www.okx.com/api/v5/market/history-candles?instId=%s&bar=1m&limit=100&after=%d" % (inst, after))
        if not rows:
            break
        for r in rows:
            out[int(r[0])] = float(r[4])
        time.sleep(0.08)
    _cache[sym] = sorted(out.items())
    return _cache[sym]


def fwd(e, mins):
    arr = candles(e.get("symbol"), 0)
    t = ts(e.get("recorded_at"))
    if not arr or t is None:
        return None
    t0 = t.timestamp() * 1000
    entry = em = None
    for ms, c in arr:
        if entry is None and ms >= t0:
            entry, em = c, ms
            continue
        if entry is not None and ms >= em + mins * 60000:
            r = (c - entry) / entry
            return r if e.get("side") == "long" else -r
    return None


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def cell(label, g):
    if not g:
        print(f"    {label:<24} n=0"); return
    parts = []
    nmax = 0
    for h in HZ:
        xs = [fwd(e, h) for e in g]; xs = [x for x in xs if x is not None]
        nmax = max(nmax, len(xs))
        if not xs:
            parts.append(f"+{h}m:—"); continue
        m = sum(xs) / len(xs); pos = sum(1 for x in xs if x > 0) / len(xs) * 100
        parts.append(f"+{h}m:{m*100:+.2f}%({pos:.0f}%/n{len(xs)})")
    print(f"    {label:<24} {'  '.join(parts)}")
    # 扣成本净 + PF(在 5m 与 15m)
    for h in (5, 15):
        xs = [fwd(e, h) for e in g]; xs = [x for x in xs if x is not None]
        if not xs:
            continue
        seg = []
        for c in COSTS:
            net = [x - c for x in xs]
            seg.append(f"@{int(c*1e4)}bps净={sum(net)/len(net)*100:+.3f}%/PF{pf(net):.2f}")
        print(f"        +{h}m(n{len(xs)}): " + "  ".join(seg))


def main():
    ev = load()
    print("=" * 96)
    print("fast-layer v1 复核(只读/反事实/signed价格收益,非实盘PnL;bybit/binance用OKX同币近似)")
    print(f"  shadow事件 n={len(ev)}")
    print("=" * 96)

    def sig(e, k):
        return (e.get("signals") or {}).get(k)

    # ---------- #1 opposite_btc ----------
    opp = [e for e in ev if sig(e, "opposite_btc")]
    print(f"\n【#1 opposite_btc 复核  命中 n={len(opp)}】")
    print("  — 总体 —")
    cell("opposite_btc(全)", opp)
    print("  — 分 venue —")
    for v in ["okx", "bybit", "binance"]:
        cell(f"venue={v}", [e for e in opp if e.get("venue") == v])
    print("  — 分 source_result —")
    for r in ["opened", "rejected", "maker_not_filled"]:
        cell(f"result={r}", [e for e in opp if e.get("source_decision_result") == r])
    print("  — 对照: 非 opposite_btc —")
    cell("non_opposite_btc", [e for e in ev if not sig(e, "opposite_btc")])

    # ---------- #4 extreme_funding ----------
    extf = [e for e in ev if (e.get("env") or {}).get("funding_bucket") == "extreme"]
    print(f"\n【#4 extreme_funding(env.funding_bucket==extreme) 复核  命中 n={len(extf)}】")
    cell("extreme_funding(全)", extf)
    for v in ["okx", "bybit", "binance"]:
        g = [e for e in extf if e.get("venue") == v]
        if g:
            cell(f"venue={v}", g)
    print("  — 对照: 非 extreme(mid/high) —")
    cell("non_extreme_funding", [e for e in ev if (e.get("env") or {}).get("funding_bucket") in ("mid", "high")])

    print(f"\n(K线缓存符号数={len(_cache)};reject_no_okx 等取不到的已自动跳过)")


if __name__ == "__main__":
    main()
