#!/usr/bin/env python3
# deep_book 专项验证(只读;反事实)。覆盖 8 维:venue / phase·capital / 成交·被拒 / horizon /
#   shallow-book 单调性 / 成本敏感性(5·10·15bps) / 实盘 outcome 对齐 / 三类结论。
#
# 局限(必须诚实):前向收益用 OKX 1m K 线(limit=300≈最近5h),signed 价格收益非实盘 PnL;
#   bybit/binance 标的也用 OKX 价近似(可能少量取不到→过滤);+15m/+30m 受"事件太新、未来K线还没到"限制,n会缩。
# 用法: SHADOW_FWD=1 python3 records/_deep_book_probe.py   (仓库根或 records/ 内均可)

import json, os, collections
from datetime import datetime, timezone, timedelta

VENUES = ["okx", "bybit", "binance", "gate"]
DEEP_TH = 100.0  # deep_book 定义: depth_cover_ratio_10bps >= 100 (与引擎 evalShadowSignals 一致)


def pos(xs):
    return sum(1 for x in xs if x > 0) / len(xs) * 100 if xs else 0.0


def avg(xs):
    return sum(xs) / len(xs) if xs else 0.0


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def load_shadow():
    ev = []
    for v in VENUES:
        for f in (f"records/{v}_directional/shadow_fast_signal_events.jsonl",
                  f"{v}_directional/shadow_fast_signal_events.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if line:
                        try:
                            ev.append(json.loads(line))
                        except json.JSONDecodeError:
                            pass
                break
    return ev


def load_settlements():
    rows = []
    for v in VENUES:
        for f in (f"records/{v}_directional/settlements.jsonl",
                  f"{v}_directional/settlements.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        r = json.loads(line); r["_venue"] = v; rows.append(r)
                    except json.JSONDecodeError:
                        pass
                break
    return rows


def _ts(s):
    s = (s or "").rstrip("Z"); s = s.split(".")[0] if "." in s else s
    try:
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


# ---------- 前向收益(网络) ----------
_cache = {}
def _candles(sym):
    if sym in _cache:
        return _cache[sym]
    import urllib.request, time as _t
    try:
        req = urllib.request.Request(
            "https://www.okx.com/api/v5/market/candles?instId=%s-SWAP&bar=1m&limit=300" % sym,
            headers={"User-Agent": "Mozilla/5.0"})
        d = json.load(urllib.request.urlopen(req, timeout=15))["data"]
        _cache[sym] = sorted([(int(r[0]), float(r[4])) for r in d])
    except Exception:
        _cache[sym] = []
    _t.sleep(0.04)
    return _cache[sym]


def fwd(e, mins):
    """事件 signed 前向收益(side 调整)。无数据/未来K线没到→None。"""
    arr = _candles(e.get("symbol")); ts = _ts(e.get("recorded_at"))
    if not arr or ts is None:
        return None
    t0 = ts.timestamp() * 1000; entry = None; em = None
    for ms, c in arr:
        if entry is None and ms >= t0:
            entry = c; em = ms; continue
        if entry is not None and ms >= em + mins * 60000:
            r = (c - entry) / entry
            return r if e.get("side") == "long" else -r
    return None


HZ = (3, 5, 15, 30)


def fwd_row(label, group, costs=(0.0,)):
    """打印一行: n + 各 horizon mean(%pos) + 指定成本下 +5m/+15m 净。"""
    cells = []
    nmax = 0
    for h in HZ:
        xs = [fwd(e, h) for e in group]; xs = [x for x in xs if x is not None]
        nmax = max(nmax, len(xs))
        if xs:
            cells.append(f"+{h}m:{avg(xs)*100:+.2f}%({pos(xs):.0f}%/n{len(xs)})")
        else:
            cells.append(f"+{h}m:—")
    print(f"    {label:<22} {' '.join(cells)}")
    if costs != (0.0,):
        for h in (5, 15):
            xs = [fwd(e, h) for e in group]; xs = [x for x in xs if x is not None]
            if not xs:
                continue
            m = avg(xs)
            seg = "  ".join(f"@{int(c*1e4)}bps净={ (m-c)*100:+.3f}%" for c in costs)
            print(f"        +{h}m(n{len(xs)}): {seg}")
    return nmax


def main():
    print("=" * 78)
    print("deep_book 专项验证(只读 / 反事实 / 非实盘 PnL)")
    print("=" * 78)
    sev = load_shadow()
    if not sev:
        print("无影子事件。"); return
    ts_all = sorted(_ts(e.get("recorded_at")) for e in sev if _ts(e.get("recorded_at")))
    span = (ts_all[-1] - ts_all[0]).total_seconds() / 60 if len(ts_all) > 1 else 0
    db = [e for e in sev if (e.get("signals") or {}).get("deep_book")]
    real = [e for e in sev if e.get("source_decision_result") in ("opened", "taker_filled")]
    print(f"影子事件 总n={len(sev)}  deep_book n={len(db)}  时间跨度≈{span:.0f}分钟 "
          f"[{ts_all[0].strftime('%m-%d %H:%M') if ts_all else '?'} ~ {ts_all[-1].strftime('%H:%M') if ts_all else '?'} UTC]")
    print(f"真实成交侧(opened+taker_filled) n={len(real)}  → 实盘成交样本极少,以下成交侧结论仅倾向")
    if span < 180:
        print("⚠ 全部事件集中在单一短窗口(<3h),无法评估跨行情/跨阶段稳定性 → 任何'正'都需继续采样确认")

    if not os.environ.get("SHADOW_FWD"):
        print("\n(需网络:用 SHADOW_FWD=1 python3 records/_deep_book_probe.py 运行前向收益部分)")
        return

    # ---- 1. 分 venue ----
    print("\n【1. 分 venue · deep_book 前向收益】")
    for v in ["okx", "bybit", "binance"]:
        g = [e for e in db if e.get("venue") == v]
        if g:
            fwd_row(f"{v}(n{len(g)})", g)
        else:
            print(f"    {v}: 无 deep_book 事件")
    print("  对照·非 deep_book(depth<100x):")
    fwd_row("non_deep_book", [e for e in sev if not (e.get("signals") or {}).get("deep_book")])

    # ---- 2. 分 phase / capital ----
    print("\n【2. 分 phase / capital】")
    print("  说明: 全部 212 级事件来自同一当前 epoch(v2+50U 之后)的单一 55 分钟窗口,")
    print("        无 D_v2/E/50U 跨阶段可分;以下按 env.total_directional_notional 粗看资金占用。")
    notio = collections.Counter()
    for e in db:
        n = (e.get("env") or {}).get("total_directional_notional")
        if n is not None:
            b = "<=15U" if n <= 15 else ("15-30U" if n <= 30 else ">30U")
            notio[b] += 1
    print(f"  deep_book 事件总名义占用分布: {dict(notio)}")

    # ---- 3. 分成交 / 被拒 ----
    print("\n【3. 分成交侧 / 被拒侧 · deep_book】")
    for res in ["opened", "taker_filled", "maker_not_filled", "rejected"]:
        g = [e for e in db if e.get("source_decision_result") == res]
        if g:
            fwd_row(f"{res}(n{len(g)})", g)
        else:
            print(f"    {res}: 0")

    # ---- 4. 分 horizon(deep_book 整体)----
    print("\n【4. 分 horizon · deep_book 整体(判断 5m edge vs 15m edge)】")
    fwd_row("deep_book", db)
    print("  注: +15m/+30m 因事件太新、未来K线尚未到齐,n 会明显缩小(见每格 n)。")

    # ---- 5. 反向验证 shallow book(单调性)----
    print("\n【5. depth_cover 分桶单调性(<20x / 20-100x / >=100x)】")
    def bucket(e):
        d = e.get("depth_cover_10bps") or 0
        return "<20x" if d < 20 else ("20-100x" if d < 100 else ">=100x")
    for b in ["<20x", "20-100x", ">=100x"]:
        g = [e for e in sev if bucket(e) == b]
        if g:
            fwd_row(f"{b}(n{len(g)})", g)
        else:
            print(f"    {b}: 0")
    print("  判定: 若 >=100x 明显优且单调递增 → 可做过滤;若不单调 → 不能上线。")

    # ---- 6. 成本敏感性 ----
    print("\n【6. 成本敏感性 · deep_book(5/10/15bps,单边×2≈往返)】")
    print("  (成本按价格收益口径:5bps=0.05% 往返)")
    fwd_row("deep_book", db, costs=(0.0005, 0.0010, 0.0015))
    print("  判定: 10-15bps 后仍正才更可靠。")

    # ---- 7. 与实盘 outcome 对齐 ----
    print("\n【7. 实盘 settlement 对齐 · deep_book 仓 vs 非 deep_book 仓】")
    rows = load_settlements()
    agg = {}
    for r in rows:
        om = r.get("open_micro")
        pid = r.get("position_id") or f'{r.get("symbol")}|{r.get("side")}|{r.get("entry",0):.8f}'
        p = agg.get(pid)
        if p is None:
            p = {"net": 0.0, "hard": False, "mfe": 0.0, "depthcov": None, "has_micro": False}
            agg[pid] = p
        p["net"] += r.get("realized_usdt", 0.0)
        if "硬止损" in r.get("reason", ""):
            p["hard"] = True
        p["mfe"] = max(p["mfe"], r.get("mfe_account_pct", 0.0))
        if isinstance(om, dict):
            p["has_micro"] = True
            if om.get("depth_cover_ratio_10bps") is not None:
                p["depthcov"] = om.get("depth_cover_ratio_10bps")
    micro_pos = [p for p in agg.values() if p["has_micro"] and p["depthcov"] is not None]
    deep = [p for p in micro_pos if p["depthcov"] >= DEEP_TH]
    shal = [p for p in micro_pos if p["depthcov"] < DEEP_TH]
    print(f"  带 depth 字段的实盘仓: {len(micro_pos)}  (deep>=100x: {len(deep)}  shallow<100x: {len(shal)})")
    for label, g in [("deep_book仓", deep), ("非deep_book仓", shal)]:
        if not g:
            print(f"    {label}: 0 样本"); continue
        nets = [p["net"] for p in g]
        hard = sum(1 for p in g if p["hard"]) / len(g) * 100
        stall = sum(1 for p in g if p["mfe"] <= 0) / len(g) * 100  # 从未浮盈≈停滞
        flag = "" if len(g) >= 20 else "  ⚠样本不足仅倾向"
        print(f"    {label:<14} n={len(g):<3} PF={pf(nets):.2f} 净={sum(nets):+.3f}U "
              f"胜率={pos(nets):.0f}% 硬止损={hard:.0f}% 从未浮盈(停滞)={stall:.0f}%{flag}")

    # ---- 8. 三类结论(自动判定 + 人复核)----
    print("\n【8. 结论(三类)】")
    db5 = [fwd(e, 5) for e in db]; db5 = [x for x in db5 if x is not None]
    db15 = [fwd(e, 15) for e in db]; db15 = [x for x in db15 if x is not None]
    net5_10 = (avg(db5) - 0.0010) * 100 if db5 else 0
    net5_15 = (avg(db5) - 0.0015) * 100 if db5 else 0
    g100 = [e for e in sev if (e.get("depth_cover_10bps") or 0) >= 100]
    g_mid = [e for e in sev if 20 <= (e.get("depth_cover_10bps") or 0) < 100]
    g_low = [e for e in sev if (e.get("depth_cover_10bps") or 0) < 20]
    def m5(g):
        xs = [fwd(e, 5) for e in g]; xs = [x for x in xs if x is not None]; return avg(xs) if xs else None
    mono = None
    a, b, c = m5(g_low), m5(g_mid), m5(g100)
    if None not in (a, b, c):
        mono = (c >= b >= a)
    print(f"  指标: deep_book +5m净@10bps={net5_10:+.3f}% @15bps={net5_15:+.3f}%  "
          f"分桶单调(<20<=20-100<=>=100)={mono}  单一窗口={span<180}  实盘deep样本={len(deep)}")
    verdict = "继续采样"
    reason = []
    if span < 180:
        reason.append("单一短窗口(无跨行情验证)")
    if len(deep) < 20:
        reason.append("实盘成交样本不足")
    if net5_15 > 0.05 and mono and span >= 180 and len(deep) >= 20:
        verdict = "可进 2B 候选"
    elif net5_10 <= -0.05 or mono is False:
        verdict = "不可靠"
        if mono is False:
            reason.append("分桶非单调")
    print(f"  → 自动判定: 【{verdict}】  依据: {('; '.join(reason)) or '阈值均满足'}")
    print("  (自动判定仅供参考,最终由人复核上面 1~7 节)")


if __name__ == "__main__":
    main()
