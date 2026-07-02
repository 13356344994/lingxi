#!/usr/bin/env python3
# fast 动态字段 forward-return 研究(只读,不改实盘)。
# 不等 settlement:直接用 fast_dynamic_feature_events 同 symbol 后续 mid 算 t+1/3/5/10/30s 前向收益,
# 逐字段派生方向(顺/逆都测),看 trade flow / book delta / liquidity 有没有高频方向 edge。
import json, os, bisect, collections

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
        t = d.get("recorded_at")
        # 解析 ns 时间戳为 epoch 秒
        s = (t or "").rstrip("Z")
        try:
            from datetime import datetime, timezone
            if "." in s:
                h, f = s.split(".", 1)
                dt = datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f")
            else:
                dt = datetime.strptime(s, "%Y-%m-%dT%H:%M:%S")
            d["_ts"] = dt.replace(tzinfo=timezone.utc).timestamp()
            d["_hour"] = dt.hour
        except ValueError:
            continue
        if d.get("mid", 0) > 0:
            ev.append(d)
    return ev


def sgn(x):
    return 1 if (x or 0) > 0 else (-1 if (x or 0) < 0 else 0)


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def build_index(ev):
    bysym = collections.defaultdict(list)
    for e in ev:
        bysym[e["symbol"]].append(e)
    for s in bysym:
        bysym[s].sort(key=lambda x: x["_ts"])
    return bysym


def fwd_ret(arr, keys, i, h):
    """arr[i] 之后 >= t+h(且不超过 t+h+gap)的最近事件 mid 相对当前 mid 的收益。"""
    t0 = arr[i]["_ts"]; m0 = arr[i]["mid"]
    target = t0 + h
    gap = h * 0.6 + 2
    j = bisect.bisect_left(keys, target)
    if j >= len(arr):
        return None
    if keys[j] > target + gap:
        return None
    return (arr[j]["mid"] - m0) / m0


# 信号 → 方向(side):+1 做多 / -1 做空 / 0 不参与。自然映射(顺字段);逆= -side。
SIGNALS = [
    ("trade_imbalance_1s(买压>0→多)", lambda e: sgn(e.get("trade_imbalance_1s"))),
    ("trade_imbalance_3s", lambda e: sgn(e.get("trade_imbalance_3s"))),
    ("trade_imbalance_5s", lambda e: sgn(e.get("trade_imbalance_5s"))),
    ("book_imbalance_now(买盘厚>0→多)", lambda e: sgn(e.get("book_imbalance_now"))),
    ("book_imbalance_delta_1s", lambda e: sgn(e.get("book_imbalance_delta_1s"))),
    ("book_imbalance_delta_3s", lambda e: sgn(e.get("book_imbalance_delta_3s"))),
    ("book_imbalance_delta_5s", lambda e: sgn(e.get("book_imbalance_delta_5s"))),
    ("mid_ret_1s(动量)", lambda e: sgn(e.get("mid_ret_1s"))),
    ("mid_ret_3s(动量)", lambda e: sgn(e.get("mid_ret_3s"))),
    ("mid_ret_5s(动量)", lambda e: sgn(e.get("mid_ret_5s"))),
    ("mid_ret_10s(动量)", lambda e: sgn(e.get("mid_ret_10s"))),
    ("bid_qty_delta_3s(bid增→多)", lambda e: sgn(e.get("best_bid_qty_delta_3s"))),
    ("ask_qty_delta_3s(ask增→空)", lambda e: -sgn(e.get("best_ask_qty_delta_3s"))),
    ("liq_pull_bid→空", lambda e: -1 if (e.get("liquidity_pull_bid") or 0) > 0 else 0),
    ("liq_pull_ask→多", lambda e: 1 if (e.get("liquidity_pull_ask") or 0) > 0 else 0),
    ("liq_refill_bid→多", lambda e: 1 if (e.get("liquidity_refill_bid") or 0) > 0 else 0),
    ("liq_refill_ask→空", lambda e: -1 if (e.get("liquidity_refill_ask") or 0) > 0 else 0),
]


def eval_signal(bysym, sidefn, invert):
    """返回该信号(顺/逆)在各 horizon 的 signed 收益(bps)列表 + 每条的 (symbol,hour,side)。"""
    out = {h: [] for h in HZ}
    meta = {h: [] for h in HZ}
    for sym, arr in bysym.items():
        keys = [e["_ts"] for e in arr]
        for i, e in enumerate(arr):
            sd = sidefn(e)
            if sd == 0:
                continue
            if invert:
                sd = -sd
            for h in HZ:
                r = fwd_ret(arr, keys, i, h)
                if r is None:
                    continue
                out[h].append(r * sd * 10000)  # bps
                meta[h].append((sym, e["_hour"], sd))
    return out, meta


def summ(bps):
    if not bps:
        return None
    n = len(bps); m = sum(bps) / n; pos = sum(1 for x in bps if x > 0) / n * 100
    return dict(n=n, mean=m, pos=pos, gpf=pf(bps),
                npf5=pf([x - 5 for x in bps]), npf10=pf([x - 10 for x in bps]),
                net5=sum(x - 5 for x in bps) / n)


def main():
    ev = load()
    bysym = build_index(ev)
    print("=" * 100)
    print(f"fast 动态 forward-return 研究(只读)  dynamic events n={len(ev)}  symbol={len(bysym)}")
    print("=" * 100)
    print("\n【1. 基础:各 horizon 原始前向收益(做多视角,基线漂移)】")
    for h in HZ:
        allr = []
        for sym, arr in bysym.items():
            keys = [e["_ts"] for e in arr]
            for i in range(len(arr)):
                r = fwd_ret(arr, keys, i, h)
                if r is not None:
                    allr.append(r * 10000)
        s = summ(allr)
        if s:
            print(f"    +{h:>2}s: n={s['n']:<6} mean={s['mean']:+.2f}bps pos={s['pos']:.0f}% PF={s['gpf']:.2f}")

    print("\n【2/3/4. 逐字段 顺/逆方向 × horizon(gross/net PF;重点看 +3/5s)】")
    cands = []
    for name, sidefn in SIGNALS:
        for invert, tag in [(False, "顺"), (True, "逆")]:
            out, meta = eval_signal(bysym, sidefn, invert)
            cells = []
            for h in [3, 5, 10]:
                s = summ(out[h])
                if s:
                    cells.append(f"+{h}s:n{s['n']} g{s['gpf']:.2f}/n5{s['npf5']:.2f}({s['mean']:+.1f}bps,{s['pos']:.0f}%)")
                    if s["n"] >= 100 and s["gpf"] > 1.3 and s["npf5"] > 1.05:
                        cands.append((name, tag, h, s, out[h], meta[h]))
            print(f"    {name:<30}[{tag}] " + "  ".join(cells))

    print("\n【5. 候选筛选(n>=100 & grossPF>1.3 & netPF@5bps>1.05)】")
    if not cands:
        print("    dynamic forward 暂未发现可交易 edge(无满足门槛的信号方向×horizon)。")
    else:
        for name, tag, h, s, bps, meta in cands:
            print(f"    ★ {name}[{tag}] +{h}s: n={s['n']} gPF={s['gpf']:.2f} nPF5={s['npf5']:.2f} nPF10={s['npf10']:.2f} mean={s['mean']:+.1f}bps pos={s['pos']:.0f}%")

    print("\n【6. 候选防假阳(按 symbol / hour / side 拆)】")
    if not cands:
        print("    (无候选,跳过)")
    for name, tag, h, s, bps, meta in cands:
        print(f"  -- {name}[{tag}]+{h}s --")
        bysy = collections.defaultdict(list); byhr = collections.defaultdict(list); bysd = collections.defaultdict(list)
        for b, (sy, hr, sd) in zip(bps, meta):
            bysy[sy].append(b); byhr[hr].append(b); bysd["long" if sd > 0 else "short"].append(b)
        nsym = sum(1 for sy in bysy if len(bysy[sy]) >= 10 and sum(bysy[sy]) > 0)
        topshare = max((len(v) for v in bysy.values()), default=0) / max(1, len(bps)) * 100
        print(f"     跨symbol:{len(bysy)}个(正贡献且n>=10:{nsym});最大单symbol占比={topshare:.0f}%")
        nhr = sum(1 for hr in byhr if sum(byhr[hr]) > 0)
        print(f"     跨hour:{len(byhr)}个(正贡献:{nhr});long PF={pf(bysd['long']):.2f}/n{len(bysd['long'])} short PF={pf(bysd['short']):.2f}/n{len(bysd['short'])}")
        verdict = "疑似真(跨symbol+跨窗+双向)" if (len(bysy) >= 5 and topshare < 40 and nhr >= 3) else "疑似假阳(集中单symbol/单窗/单向)"
        print(f"     → {verdict}")

    # 磁量字段诊断(无方向,看是否预示更大波动)
    print("\n【附. 量级字段 → 后续 |mid_ret_5s| 均值(高活跃是否更大波动;无方向)】")
    for f in ["trade_count_5s", "large_trade_count", "large_trade_notional"]:
        hi = [e for e in ev if (e.get(f) or 0) > 0]
        # 简易:高活跃组 vs 全体的 3s 绝对波动
        def absmove(sub):
            vals = []
            for sym in set(e["symbol"] for e in sub):
                arr = bysym[sym]; keys = [x["_ts"] for x in arr]
                idx = {round(x["_ts"], 3): k for k, x in enumerate(arr)}
                for e in sub:
                    if e["symbol"] != sym:
                        continue
                    k = idx.get(round(e["_ts"], 3))
                    if k is None:
                        continue
                    r = fwd_ret(arr, keys, k, 5)
                    if r is not None:
                        vals.append(abs(r) * 10000)
            return (sum(vals) / len(vals)) if vals else 0
        print(f"    {f:<20} 有值n={len(hi)} 该组后续|+5s|均={absmove(hi):.1f}bps")


if __name__ == "__main__":
    main()
