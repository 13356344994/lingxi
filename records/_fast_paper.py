#!/usr/bin/env python3
# M5: fast scalper v1 paper trade(只读 / 不真实下单)。
# 入场:fast event 的 record-time 分数(fast_candidate_score>=阈值 且 side in long/short)——只用当前时刻分数,不看未来。
# 出场:固定 horizon 30s/60s/180s,用同 symbol 后续快照 mid 平仓(paper)。
# 输出:各 horizon paper PF / 均值 / 正占比 / 扣 5-10bps 后 PF;并写 fast_paper_trades.jsonl(假开/假平)。
# 明确:M5 前后禁止真实下单;数据不足时不据此上线。
import json, os, sys
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
HORIZONS = [30, 60, 180]
TOL = 8
THRESH = float(sys.argv[1]) if len(sys.argv) > 1 else 0.6
OUT = "okx_directional/fast_paper_trades.jsonl"


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
                d = json.loads(l); d["_t"] = ts(d.get("recorded_at"))
                if d["_t"] and d.get("mid", 0) > 0:
                    ev.append(d)
            except json.JSONDecodeError:
                pass
    return ev


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def exit_mid(by_sym, e, w):
    seq = by_sym[(e["venue"], e["symbol"])]
    target = e["_t"].timestamp() + w
    for x in seq:
        xt = x["_t"].timestamp()
        if xt >= target and xt <= target + TOL + w * 0.05:
            return x["mid"], x["recorded_at"]
    return None, None


def main():
    ev = load()
    print("=" * 88)
    print(f"M5 fast paper trade(只读/不下单;入场阈值 score>={THRESH};出场 30/60/180s)")
    if not ev:
        print("  无 fast 事件。"); return
    by_sym = {}
    for e in ev:
        by_sym.setdefault((e["venue"], e["symbol"]), []).append(e)
    for k in by_sym:
        by_sym[k].sort(key=lambda x: x["_t"])

    scored = [e for e in ev if e.get("fast_candidate_score", 0) >= THRESH and e.get("fast_candidate_side") in ("long", "short")]
    span = (max(e["_t"] for e in ev) - min(e["_t"] for e in ev)).total_seconds() / 60
    print(f"  事件 n={len(ev)}  跨度={span:.1f}min  入场候选(score>={THRESH}&有向)={len(scored)}")
    print("=" * 88)
    if not scored:
        print("  暂无达阈值的入场候选(可能 M4 scorer 尚未部署或分数偏低)。降低阈值: python3 _fast_paper.py 0.4")
        return

    trades = []
    for w in HORIZONS:
        rets = []
        for e in scored:
            xm, xat = exit_mid(by_sym, e, w)
            if xm is None:
                continue
            r = (xm - e["mid"]) / e["mid"] * 10000  # bps,做多视角
            if e["fast_candidate_side"] == "short":
                r = -r
            rets.append(r)
            if w == 60:  # 只对 60s horizon 落一份 paper 成交记录(避免重复)
                trades.append({"recorded_at": e.get("recorded_at"), "venue": e["venue"], "symbol": e["symbol"],
                               "side": e["fast_candidate_side"], "score": e.get("fast_candidate_score"),
                               "entry_mid": e["mid"], "exit_mid": xm, "exit_at": xat,
                               "horizon_s": w, "ret_bps": round(r, 3)})
        if not rets:
            print(f"  +{w}s: 无足够后续快照(n=0)"); continue
        mean = sum(rets) / len(rets); pos = sum(1 for x in rets if x > 0) / len(rets) * 100
        seg = []
        for c in (0, 5, 10):
            net = [x - c for x in rets]
            seg.append(f"@{c}bps:paperPF{pf(net):.2f}/净{sum(net)/len(net):+.2f}bps")
        print(f"  +{w}s: n={len(rets):<4} 均值={mean:+.2f}bps 正占比={pos:.0f}%  " + "  ".join(seg))

    if trades and os.environ.get("WRITE_PAPER") == "1":
        with open(OUT, "w") as f:
            for t in trades:
                f.write(json.dumps(t, ensure_ascii=False) + "\n")
        print(f"\n  已写 paper 成交 {len(trades)} 条 → {OUT}(WRITE_PAPER=1)")
    else:
        print(f"\n  (paper 成交未落盘;设 WRITE_PAPER=1 可写 {OUT})")
    print("  ⚠ 数据不足/单一窗口,paper PF 仅占位,M5 门槛未到,禁止据此接实盘。")


if __name__ == "__main__":
    main()
