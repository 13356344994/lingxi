#!/usr/bin/env python3
# 低效率 universe 候选「上线前置评估」(只读,不接实盘)。
# 对象:ACT/ALGO/AGLD/AR + universe monitor 发现的所有 ★ 达标/接近达标长尾币。
# 每 symbol 输出 7 段:样本完整性 / lead源拆分 / horizon拆分(重点 net@6.2)/ 成本结构 /
#   symbol稳定性 / 假阳性检查 / 结论(PASS_LIVE_CANDIDATE|WATCH_MORE|FAIL_COST|FAIL_STABILITY|FAIL_SAMPLE)。
# 内存有界:流式读取,只保留候选 symbol 的行。
import json, os, bisect, collections, re, sys
from datetime import datetime, timezone

PATH = "crossvenue/fast_peer_leadlag_events.jsonl"
MON = "_fast_universe_monitor.out"
FIXED = ["ACT-USDT", "ALGO-USDT", "AGLD-USDT", "AR-USDT"]
ALIGN = 1.5
PEER_MOVE_BP = 3.0
OKX_STILL_BP = 1.0
HZ = [5, 10, 30, 60]
COST_REAL = 6.2       # 当前真实往返成本(线二实测)
COST_MM = 2.1         # maker+maker 理论成本(线二)
COSTS = [2.1, 5.0, 6.2, 10.0]
# 上线门槛
MIN_N = 100; MIN_MEAN = 5.0; MIN_GPF = 1.3; MIN_NET_REAL = 1.05; MIN_POS_WINDOWS = 3


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


def netpf(xs, c):
    return pf([x - c for x in xs])


def discover_symbols():
    syms = set(FIXED)
    if os.path.exists(MON):
        for l in open(MON):
            if "★" in l or "UNIV_CANDIDATE" in l:
                for m in re.findall(r"([A-Z0-9]+-USDT)", l):
                    syms.add(m)
    return syms


def main():
    SYMS = discover_symbols()
    print(f"候选 symbol({len(SYMS)}):{sorted(SYMS)}")
    # 流式:只保留候选 symbol 的行
    okx = collections.defaultdict(list)   # sym -> [(t,mid,spread,mr3)]
    peer = collections.defaultdict(list)  # sym -> [(venue,t,mr3,mid,tcount5)]
    for l in open(PATH):
        l = l.strip()
        if not l:
            continue
        try:
            d = json.loads(l)
        except json.JSONDecodeError:
            continue
        sym = d.get("symbol")
        if sym not in SYMS:
            continue
        mid = d.get("mid", 0) or 0
        t = ts(d.get("recorded_at"))
        if mid <= 0 or t is None:
            continue
        v = d.get("venue")
        if v == "okx":
            okx[sym].append((t, mid, d.get("spread_bps", 0) or 0, d.get("mid_ret_3s", 0) or 0))
        elif v == "binance" or v == "bybit":
            peer[sym].append((sys.intern(v), t, d.get("mid_ret_3s", 0) or 0, mid, d.get("trade_count_5s", 0) or 0))

    results = {}
    for sym in sorted(SYMS):
        orows = okx.get(sym); prows = peer.get(sym)
        if not orows or not prows:
            results[sym] = ("FAIL_SAMPLE", "无 OKX 或 peer 数据"); continue
        orows.sort(key=lambda x: x[0])
        OT = [r[0] for r in orows]; OM = [r[1] for r in orows]; OSP = [r[2] for r in orows]; OR3 = [r[3] for r in orows]
        T0, T1 = OT[0], OT[-1]

        def now_i(t):
            i = bisect.bisect_left(OT, t); best = None
            for j in (i - 1, i):
                if 0 <= j < len(OT) and abs(OT[j] - t) <= ALIGN and (best is None or abs(OT[j] - t) < abs(OT[best] - t)):
                    best = j
            return best

        def fwd(i0, h):
            tgt = OT[i0] + h; gap = max(2.0, h * 0.5); j = bisect.bisect_left(OT, tgt)
            if j >= len(OT) or OT[j] > tgt + gap or OM[i0] <= 0:
                return None
            return (OM[j] - OM[i0]) / OM[i0]

        # 采样 lag-follow:peer 已动 & OKX 未动;记录 (side, t, i0, leadvenue)
        samples = []
        for v, t, mr3, mid, tc in prows:
            mv = mr3 * 1e4
            if abs(mv) < PEER_MOVE_BP:
                continue
            i0 = now_i(t)
            if i0 is None or abs(OR3[i0] * 1e4) > OKX_STILL_BP:
                continue
            samples.append((sgn(mv), t, i0, v))

        # 每 horizon 的 signed bps + meta
        def bps_at(h, subset=None):
            out = []; meta = []
            for side, t, i0, v in samples:
                if subset and v != subset:
                    continue
                r = fwd(i0, h)
                if r is None:
                    continue
                out.append(r * side * 1e4); meta.append((t, v))
            return out, meta

        print("\n" + "=" * 78)
        print(f"### {sym}   OKX点={len(orows)} peer点={len(prows)} lag-follow命中={len(samples)} 跨度={(T1-T0)/60:.0f}min")

        # ---- 3. horizon 拆分(先算,后面结论要用) ----
        hz_stat = {}
        for h in HZ:
            b, meta = bps_at(h)
            if not b:
                hz_stat[h] = None; continue
            n = len(b); mean = sum(b) / n; g = pf(b); pos = sum(1 for x in b if x > 0) / n * 100
            hz_stat[h] = {"n": n, "mean": mean, "g": g, "pos": pos, "bps": b, "meta": meta,
                          "net5": netpf(b, 5), "net62": netpf(b, 6.2), "net10": netpf(b, 10)}
        # 主 horizon = 30s(其次 60s)
        H = 30 if hz_stat.get(30) else (60 if hz_stat.get(60) else None)

        # ---- 1. 样本完整性 ----
        print("[1] 样本完整性:")
        if H:
            st = hz_stat[H]
            byw = collections.defaultdict(list)
            for x, (t, v) in zip(st["bps"], st["meta"]):
                byw[min(2, int((t - T0) / (T1 - T0 + 1e-9) * 3))].append(x)
            byh = collections.defaultdict(list)
            for x, (t, v) in zip(st["bps"], st["meta"]):
                byh[int(t // 3600)].append(x)
            wpos3 = sum(1 for v in byw.values() if sum(v) > 0)
            hpos = sum(1 for v in byh.values() if sum(v) > 0); hn = len(byh)
            topw = max((len(v) for v in byw.values()), default=0) / max(1, st["n"]) * 100
            print(f"    主horizon={H}s n={st['n']} 时间跨度={(T1-T0)/60:.0f}min 三等分正窗口={wpos3}/3 小时正窗口={hpos}/{hn} 最大三分段占比={topw:.0f}%")
        else:
            print("    无有效 horizon 样本"); results[sym] = ("FAIL_SAMPLE", "无对齐样本"); continue

        # ---- 2. lead 源拆分(30s) ----
        print("[2] lead 源拆分(主horizon):")
        lead_stat = {}
        for v in ("binance", "bybit"):
            b, _ = bps_at(H, subset=v)
            if b:
                lead_stat[v] = (len(b), sum(b) / len(b), netpf(b, COST_REAL))
                print(f"    {v}-led: n={len(b)} mean={sum(b)/len(b):+.1f}bp net@6.2={netpf(b,COST_REAL):.2f}")
            else:
                print(f"    {v}-led: 无样本")
        # 共振:binance 事件附近(<=1s)有同向 bybit
        byb_t = sorted([(t, sgn(mr3)) for (v, t, mr3, mid, tc) in prows if v == "bybit"])
        bts = [x[0] for x in byb_t]; bsd = [x[1] for x in byb_t]
        reson = []
        for side, t, i0, v in samples:
            if v != "binance":
                continue
            k = bisect.bisect_left(bts, t); ok = False
            for j in (k - 1, k):
                if 0 <= j < len(bts) and abs(bts[j] - t) <= 1.0 and bsd[j] == side:
                    ok = True; break
            if ok:
                r = fwd(i0, H)
                if r is not None:
                    reson.append(r * side * 1e4)
        if reson:
            print(f"    共振(bin+byb同向): n={len(reson)} mean={sum(reson)/len(reson):+.1f}bp net@6.2={netpf(reson,COST_REAL):.2f}")
        main_lead = max(lead_stat, key=lambda v: lead_stat[v][1]) if lead_stat else None
        if main_lead:
            print(f"    主要贡献 lead 源 = {main_lead}")

        # ---- 3. horizon 拆分打印 ----
        print("[3] horizon 拆分(mean/gPF/net@5/net@6.2/net@10/pos):")
        for h in HZ:
            st = hz_stat[h]
            if not st:
                print(f"    {h}s: —"); continue
            print(f"    {h}s: n={st['n']} mean={st['mean']:+.1f}bp gPF={st['g']:.2f} net@5={st['net5']:.2f} net@6.2={st['net62']:.2f} net@10={st['net10']:.2f} pos={st['pos']:.0f}%")

        # ---- 4. 成本结构 ----
        print("[4] 成本结构(主horizon):")
        st = hz_stat[H]
        # 打平所需最低成本(net PF>1.05 的最大 c)
        breakeven = None
        for c in [10, 8, 6.2, 5, 4, 3, 2.1, 1, 0.5]:
            if netpf(st["bps"], c) > 1.05:
                breakeven = c; break
        print(f"    maker+taker@6.2bp: net@6.2={st['net62']:.2f} → {'可交易' if st['net62']>1.05 else '不可(<1.05)'}")
        print(f"    maker+maker@2.1bp: net@2.1={netpf(st['bps'],2.1):.2f} → {'可交易' if netpf(st['bps'],2.1)>1.05 else '不可'}(依赖 maker 出场成交,全局 sim:+2bp档成交≈73%、未成交中位回撤≈-14bp)")
        print(f"    可过成本(net>1.05)最高成本 ≈ {breakeven if breakeven else '<0.5'}bp;需把往返成本压到 ~{breakeven if breakeven else 1}bp 以内")

        # ---- 5. symbol 稳定性 ----
        print("[5] symbol 稳定性:")
        spr_sorted = sorted(OSP)
        spmed = spr_sorted[len(spr_sorted) // 2]; sp90 = spr_sorted[int(len(spr_sorted) * 0.9)]
        vc = collections.Counter(v for (v, t, mr3, mid, tc) in prows)
        # 各 venue 最大间隔(WS 连续性)
        gaps = {}
        for v in ("binance", "bybit"):
            vts = sorted(t for (vv, t, mr3, mid, tc) in prows if vv == v)
            gaps[v] = max((vts[i + 1] - vts[i] for i in range(len(vts) - 1)), default=0) if len(vts) > 1 else 0
        okxts = OT; okxgap = max((okxts[i + 1] - okxts[i] for i in range(len(okxts) - 1)), default=0)
        notrade = sum(1 for (v, t, mr3, mid, tc) in prows if v == "bybit" and tc == 0)
        nbyb = max(1, vc.get("bybit", 0))
        print(f"    spread 中位={spmed:.1f}bp p90={sp90:.1f}bp;三所事件数 okx={len(orows)} binance={vc.get('binance',0)} bybit={vc.get('bybit',0)}")
        print(f"    最大数据间隔: okx={okxgap:.0f}s binance={gaps['binance']:.0f}s bybit={gaps['bybit']:.0f}s;bybit无成交(tc5s=0)占比={notrade/nbyb*100:.0f}%")
        # 注:单次 ~72s 缺口是服务重启造成(三所同步),非 WS 不稳;阈值放宽到容忍重启(<300s)。
        ws_stable = spmed <= 20 and sp90 <= 40 and okxgap < 300 and gaps["binance"] < 300 and gaps["bybit"] < 300 and vc.get("binance", 0) > 0 and vc.get("bybit", 0) > 0

        # ---- 6. 假阳性检查 ----
        print("[6] 假阳性检查(主horizon):")
        b = sorted(st["bps"]); n = len(b)
        trim1 = b[:max(1, int(n * 0.99))]  # 去掉最大 1%
        trim5 = b[:max(1, int(n * 0.95))]  # 去掉最大 5%
        m1 = sum(trim1) / len(trim1); m5 = sum(trim5) / len(trim5)
        byh = collections.defaultdict(list)
        for x, (t, v) in zip(st["bps"], st["meta"]):
            byh[int(t // 3600)].append(x)
        hpos = sum(1 for v in byh.values() if sum(v) > 0); hn = len(byh)
        single_lead = (len(lead_stat) <= 1) or (main_lead and lead_stat.get(main_lead) and lead_stat[main_lead][0] / max(1, st["n"]) > 0.85)
        print(f"    去top1%后mean={m1:+.1f}bp 去top5%后mean={m5:+.1f}bp(原{st['mean']:+.1f});小时正窗口={hpos}/{hn};是否单lead源主导={'是' if single_lead else '否'}")

        # ---- 7. 结论 ----
        st30 = hz_stat.get(30) or hz_stat.get(60)
        reason = []
        verdict = "PASS_LIVE_CANDIDATE"
        if st30["n"] < MIN_N:
            verdict = "FAIL_SAMPLE"; reason.append(f"n={st30['n']}<100")
        elif not ws_stable:
            verdict = "FAIL_STABILITY"; reason.append("spread/WS 不稳")
        elif st30["net62"] <= MIN_NET_REAL:
            verdict = "FAIL_COST"; reason.append(f"net@6.2={st30['net62']:.2f}<=1.05")
        else:
            # 稳健性(否则 WATCH_MORE)
            if st30["mean"] < MIN_MEAN or st30["g"] <= MIN_GPF:
                verdict = "WATCH_MORE"; reason.append("mean/gPF 边际")
            if hpos < MIN_POS_WINDOWS:
                verdict = "WATCH_MORE"; reason.append(f"正小时窗口{hpos}<3")
            if m5 < MIN_MEAN:
                verdict = "WATCH_MORE"; reason.append("去极值后mean<5")
            if single_lead:
                verdict = "WATCH_MORE"; reason.append("单lead源主导")
        results[sym] = (verdict, "; ".join(reason) or "满足全部门槛")
        print(f"[7] 结论: {verdict}  ({results[sym][1]})")

    print("\n" + "#" * 78)
    print("汇总:")
    for sym in sorted(results):
        v, r = results[sym]
        print(f"  {sym:<12} {v:<22} {r}")
    print("\n门槛: n>=100 & mean>=5bp & gPF>1.3 & net@真实成本6.2>1.05 & >=3小时窗口正 & 去极值仍达标 & WS稳定 & 不单lead源主导")
    print("注:只读评估,不接实盘;PASS 也需人工确认成本/执行方式后才讨论 fast live。")


if __name__ == "__main__":
    main()
