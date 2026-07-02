#!/usr/bin/env python3
# 事件驱动信号研究 v1:大单冲击(trade burst)→ OKX 后续位移(只读,不下单,不改任何实盘)。
# 背景(HANDOFF 2026-07-02):fast 重开硬门槛=信号单笔位移 ≥4bp(maker+maker 混合真实成本 ≈2.9bp)。
# lag-follow(peer 已动→OKX 跟)已被大样本否决;本脚本换事件类:**单笔/1s 级成交量爆发**是否带来
# ≥4bp 的可交易位移(顺势=continuation 为正;反着看即 fade/回归)。
#
# 事件定义(venue=okx):
#   notional_1s = (buy+sell qty_1s)×mid ≥ TIER(分档)且单边占比 |buy-sell|/(buy+sell) ≥ IMB_MIN
#   方向 = 爆发方向(买爆发=+1);同 symbol 30s 内只取首个事件(去重防膨胀 n)。
# 输出:各档 n / mean / 中位 / 正率 / grossPF(顺势),horizon 5/10/30/60s;spread≤8bp 可交易过滤。
# 纪律:探索性研究(多档多窗=多重比较),任何"达标"须再过 [n≥100 & 跨时间窗稳定 & mean≥4bp] 才算候选。
import json, sys, collections
from datetime import datetime, timezone

PATH = "crossvenue/fast_peer_leadlag_events.jsonl"
TIERS = [1000.0, 3000.0, 10000.0, 30000.0]   # 1s 成交名义档(USDT)
IMB_MIN = 0.7
HZ = [5, 10, 30, 60]
DEDUP_S = 30.0
MAX_SPREAD_BP = 8.0
COST_MIX_BP = 2.9   # maker+maker 混合真实成本(T=1bp 档)
COST_TAKER_BP = 6.2 # 当前实测 taker 出场成本


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6],
                                     "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc).timestamp()
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()
    except ValueError:
        return None


def main():
    # pending[sym] = list of dict(event);逐流解析,未来样本到达时回填 horizon mid。
    pending = collections.defaultdict(list)
    last_event_at = {}
    done = []
    n_rows = 0
    t0 = t1 = None

    for line in open(PATH):
        line = line.strip()
        if not line:
            continue
        try:
            d = json.loads(line)
        except json.JSONDecodeError:
            continue
        if d.get("venue") != "okx":
            continue
        t = ts(d.get("recorded_at"))
        mid = d.get("mid") or 0
        if t is None or mid <= 0:
            continue
        n_rows += 1
        if t0 is None:
            t0 = t
        t1 = t
        sym = d.get("symbol")

        # 1) 回填该 symbol 的 pending 事件
        keep = []
        for ev in pending[sym]:
            dt = t - ev["t"]
            for h in HZ:
                if ev["fwd"].get(h) is None and dt >= h:
                    ev["fwd"][h] = ev["dir"] * (mid / ev["mid"] - 1.0) * 1e4
            if dt > HZ[-1] + 3:
                done.append(ev)
            else:
                keep.append(ev)
        pending[sym] = keep

        # 2) 事件检测
        buy = d.get("trade_buy_qty_1s") or 0.0
        sell = d.get("trade_sell_qty_1s") or 0.0
        tot = buy + sell
        if tot <= 0:
            continue
        notional = tot * mid
        if notional < TIERS[0]:
            continue
        imb = (buy - sell) / tot
        if abs(imb) < IMB_MIN:
            continue
        if t - last_event_at.get(sym, 0) < DEDUP_S:
            continue
        spread = d.get("spread_bps") or 999
        last_event_at[sym] = t
        pending[sym].append({
            "t": t, "sym": sym, "mid": mid, "dir": 1 if imb > 0 else -1,
            "notional": notional, "spread": spread, "fwd": {},
        })

    for evs in pending.values():
        done.extend(ev for ev in evs if any(v is not None for v in ev["fwd"].values()))

    hours = (t1 - t0) / 3600 if (t0 and t1) else 0
    print(f"okx 行数={n_rows}  数据窗≈{hours:.1f}h  冲击事件(去重后,≥{TIERS[0]:.0f}U & |imb|≥{IMB_MIN})={len(done)}")
    print(f"成本对照:maker+maker混合≈{COST_MIX_BP}bp / taker出场≈{COST_TAKER_BP}bp;fast 重开门槛=位移≥4bp\n")

    def report(evs, label):
        print(f"—— {label}(n={len(evs)}) ——")
        if len(evs) < 30:
            print("   ⚠ 样本不足(<30),仅倾向\n")
        for h in HZ:
            xs = [ev["fwd"][h] for ev in evs if ev["fwd"].get(h) is not None]
            if not xs:
                continue
            xs_s = sorted(xs)
            mean = sum(xs) / len(xs)
            med = xs_s[len(xs_s) // 2]
            posr = sum(1 for x in xs if x > 0) / len(xs)
            g = sum(x for x in xs if x > 0)
            l = abs(sum(x for x in xs if x < 0)) or 1e-9
            print(f"   +{h:>2}s: n={len(xs):<6} mean={mean:+6.2f}bp  中位={med:+6.2f}bp  正率={posr:4.0%}  grossPF={g/l:5.2f}"
                  f"  net@{COST_MIX_BP}={mean-COST_MIX_BP:+5.2f}bp")
        print()

    # 分档(可交易过滤后)
    tradable = [ev for ev in done if ev["spread"] <= MAX_SPREAD_BP]
    print(f"[可交易过滤 spread≤{MAX_SPREAD_BP}bp] {len(tradable)}/{len(done)}\n")
    for i, tier in enumerate(TIERS):
        hi = TIERS[i + 1] if i + 1 < len(TIERS) else float("inf")
        evs = [ev for ev in tradable if tier <= ev["notional"] < hi]
        report(evs, f"顺势 continuation | 冲击名义 [{tier:.0f}, {hi if hi != float('inf') else '∞'})U")

    # 反向解读:fade(冲击回归)= 上面取负。给最大两档的 fade 视图。
    big = [ev for ev in tradable if ev["notional"] >= TIERS[-2]]
    inv = [{**ev, "fwd": {h: (None if v is None else -v) for h, v in ev["fwd"].items()}} for ev in big]
    report(inv, f"反向 fade | 冲击名义 ≥{TIERS[-2]:.0f}U(顺势取负)")

    print("判据(候选须全部满足,且需跨 ≥2 个不重叠时间窗复核):n≥100 & |mean|≥4bp & grossPF≥1.3 & 正率≥55%")
    print("⚠ 本脚本为多档×多窗探索(多重比较未校正);mid 反事实无成交/滑点,达标也只进入下一轮验证,不直接上线。")


if __name__ == "__main__":
    main()
