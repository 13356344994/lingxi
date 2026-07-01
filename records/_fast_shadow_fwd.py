#!/usr/bin/env python3
# M3: fast_executable_shadow_events 前向收益回填/统计(只读)。
# 思路:recorder 每 ~4s 对同一批 symbol 拍快照,故"前向收益"=同 symbol 在 t+15/30/60/180s 的快照 mid 相对 t 的变动(bps)。
#   对每条事件 t,在同 symbol 的快照序列里找 >= t+window 的最早一条(容差 ±loop间隔),用其 mid 算 signed 收益。
# 方向:fast 事件本身无 side;这里默认"做多视角"(mid 上行=正)。后续 M4 scorer 再按信号定方向。
# 输出:各 horizon 的 n / 均值bps / 正占比 / PF / 扣 5/10bps 后 PF。纯只读,不改实盘、不改事件文件。
import json, os, sys
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
WINDOWS = [15, 30, 60, 180]
TOL = 8  # 容差秒(loop ~4s,取 >=target 的最早点,且不超过 target+TOL)


def ts(s):
    s = (s or "").rstrip("Z")
    if "." in s:
        h, f = s.split(".", 1); s = h + "." + "".join(c for c in f if c.isdigit())[:6]; fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    try:
        return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def load():
    ev = []
    for v in VEN:
        f = f"{v}_directional/fast_executable_shadow_events.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            l = l.strip()
            if not l:
                continue
            try:
                d = json.loads(l)
                d["_t"] = ts(d.get("recorded_at"))
                if d["_t"] and d.get("mid", 0) > 0:
                    ev.append(d)
            except json.JSONDecodeError:
                pass
    return ev


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def fwd_for(events_by_sym, e, w):
    """同 symbol 序列里找 t+w(±TOL)的最早快照,返回 signed bps(做多视角)。"""
    seq = events_by_sym[(e["venue"], e["symbol"])]
    t0 = e["_t"].timestamp(); target = t0 + w
    best = None
    for x in seq:
        xt = x["_t"].timestamp()
        if xt >= target and xt <= target + TOL + w * 0.05:
            best = x; break
    if best is None:
        return None
    return (best["mid"] - e["mid"]) / e["mid"] * 10000


def report(label, group, by_sym):
    print(f"\n  [{label}] n_events={len(group)}")
    for w in WINDOWS:
        xs = [fwd_for(by_sym, e, w) for e in group]
        xs = [x for x in xs if x is not None]
        if not xs:
            print(f"    +{w:>3}s: (无足够后续快照,n=0)"); continue
        mean = sum(xs) / len(xs); pos = sum(1 for x in xs if x > 0) / len(xs) * 100
        seg = []
        for c in (0, 5, 10):
            net = [x - c for x in xs]
            seg.append(f"@{c}bps:PF{pf(net):.2f}/净{sum(net)/len(net):+.2f}bps")
        print(f"    +{w:>3}s: n={len(xs):<4} 均值={mean:+.2f}bps 正占比={pos:.0f}%  " + "  ".join(seg))


def main():
    ev = load()
    print("=" * 90)
    print("M3 fast shadow 前向收益(只读;做多视角;同symbol后续快照算fwd;非实盘PnL)")
    if not ev:
        print("  无 fast 事件(recorder 未跑或文件空)。"); return
    span = (max(e["_t"] for e in ev) - min(e["_t"] for e in ev)).total_seconds() / 60
    print(f"  事件 n={len(ev)}  时间跨度={span:.1f}min  symbol数={len(set((e['venue'],e['symbol']) for e in ev))}")
    print("=" * 90)
    by_sym = {}
    for e in ev:
        by_sym.setdefault((e["venue"], e["symbol"]), []).append(e)
    for k in by_sym:
        by_sym[k].sort(key=lambda x: x["_t"])
    report("全体", ev, by_sym)
    # 几个简单子集预览(供 M4 scorer 参考)
    report("maker_placeable", [e for e in ev if e.get("maker_placeable")], by_sym)
    report("窄spread<=3bps", [e for e in ev if 0 < e.get("spread_bps", 1e9) <= 3], by_sym)
    report("imbalance>0.3(买盘厚)", [e for e in ev if e.get("orderbook_imbalance", 0) > 0.3], by_sym)
    report("imbalance<-0.3(卖盘厚)", [e for e in ev if e.get("orderbook_imbalance", 0) < -0.3], by_sym)
    print("\n注:目标=找 30s-180s 扣 5-10bps 后 PF>1 的子集;当前数据少时仅占位,随 recorder 累积。")


if __name__ == "__main__":
    main()
