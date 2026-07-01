#!/usr/bin/env python3
# 线二 Cost Structure Review(只读,不买币/不开抵扣/不改资金)。
# 目标:确认 fast 往返成本能不能压到 1-2bps,以匹配 lag-follow 约 2.1bps 的位移。
# 基于真实 fast settlements + OKX published 费率 + fee audit。
import json, os, collections

SETTLE = "okx_directional/settlements.jsonl"
FEEAUDIT = "fee_discount_audit.jsonl"
# OKX published(fee audit 记录;账户级 VIP/OKB 抵扣 = NEED_API)
OKX_MAKER_BP = 2.0
OKX_TAKER_BP = 5.0
LAGFOLLOW_BP = 2.1  # 线一/放大研究里 lag-follow 30s 的位移


def pct(xs, p):
    if not xs:
        return 0.0
    xs = sorted(xs); i = min(len(xs) - 1, int(len(xs) * p))
    return xs[i]


def main():
    # fee audit 费率
    global OKX_MAKER_BP, OKX_TAKER_BP
    if os.path.exists(FEEAUDIT):
        for l in open(FEEAUDIT):
            try:
                d = json.loads(l)
                if d.get("venue") == "okx":
                    OKX_MAKER_BP = d["maker_fee"] * 1e4; OKX_TAKER_BP = d["taker_fee"] * 1e4
            except Exception:
                pass
    print(f"OKX published 费率(fee audit):maker={OKX_MAKER_BP:.1f}bp taker={OKX_TAKER_BP:.1f}bp  (账户 VIP/OKB 抵扣=NEED_API)")

    rows = []
    for l in open(SETTLE):
        l = l.strip()
        if not l or "fast" not in l:
            continue
        try:
            d = json.loads(l)
        except json.JSONDecodeError:
            continue
        if "fast" not in str(d.get("entry_layer", "")) and "fast" not in str(d.get("signal_layer", "")):
            continue
        rows.append(d)
    print(f"fast settlements={len(rows)}")
    if not rows:
        print("无 fast 结算。"); return

    fee_bps, spread_bps, maker_edge, maker_entry_ct, taker_exit_ct, notional_sum = [], [], [], 0, 0, 0.0
    realized_cost = []  # 实际往返成本 bps(近似)= fee + 出场吃单半spread - 入场 maker edge
    for d in rows:
        notl = d.get("notional", 0) or 0
        fee = d.get("fee_usdt", 0) or 0
        if notl > 0:
            fb = fee / notl * 1e4
            fee_bps.append(fb)
            notional_sum += notl
        sp = d.get("entry_spread_bps", 0) or 0
        if sp > 0:
            spread_bps.append(sp)
        me = d.get("maker_edge_bps")
        if d.get("maker_entry"):
            maker_entry_ct += 1
            if me is not None:
                maker_edge.append(me)
        if d.get("close_via_taker"):
            taker_exit_ct += 1
        # 实际成本近似
        if notl > 0:
            fb = fee / notl * 1e4
            exit_half = (sp / 2.0) if d.get("close_via_taker") and sp > 0 else 0.0
            ent_edge = me if (d.get("maker_entry") and me is not None) else 0.0
            realized_cost.append(fb + exit_half - ent_edge)

    med_fee = pct(fee_bps, 0.5); med_sp = pct(spread_bps, 0.5); med_edge = pct(maker_edge, 0.5)
    print(f"\n样本结构:maker入场={maker_entry_ct}/{len(rows)}  taker出场={taker_exit_ct}/{len(rows)}  名义合计={notional_sum:.1f}U")
    print(f"中位数:实际fee={med_fee:.2f}bp  入场spread={med_sp:.2f}bp  maker_edge(入场省)={med_edge:.2f}bp")

    print("\n【往返成本(bps,越低越好)】")
    print(f"  (a) 当前实盘实测(maker入场+taker出场,含实际fee)  中位≈{pct(realized_cost,0.5):+.2f}  均值≈{sum(realized_cost)/len(realized_cost):+.2f}  (n={len(realized_cost)})")
    # (b) published 费率理论
    b_makertaker = OKX_MAKER_BP + OKX_TAKER_BP + med_sp / 2.0 - med_edge
    print(f"  (b) maker入场+taker出场(published):maker{OKX_MAKER_BP:.1f}+taker{OKX_TAKER_BP:.1f}+出场半spread{med_sp/2:.1f}-maker_edge{med_edge:.1f} = {b_makertaker:+.2f}bp")
    # (c) maker+maker 理论(两腿都挂单,不吃 spread;两腿都付 maker fee,理论各省 maker_edge)
    c_makermaker = OKX_MAKER_BP * 2 - med_edge * 2
    print(f"  (c) maker入场+maker出场(理论):maker*2({OKX_MAKER_BP*2:.1f}) - maker_edge*2({med_edge*2:.1f}) = {c_makermaker:+.2f}bp   [注:maker 出场有不成交风险,是理论下限]")

    print("\n【要交易 lag-follow(位移≈{:.1f}bp)需要什么成本?】".format(LAGFOLLOW_BP))
    print(f"  盈利条件:往返成本 < {LAGFOLLOW_BP:.1f}bp")
    for name, cost in [("当前实测", pct(realized_cost, 0.5)), ("maker+taker(published)", b_makertaker), ("maker+maker(理论)", c_makermaker)]:
        verdict = "✓ 可过成本" if cost < LAGFOLLOW_BP else f"✗ 缺口 {cost-LAGFOLLOW_BP:+.1f}bp"
        print(f"    {name:<24} 成本{cost:+.2f}bp → {verdict}")
    # 需要的 maker fee(maker+maker 且 edge 不变)
    need_maker = (LAGFOLLOW_BP + med_edge * 2) / 2.0
    print(f"\n  若走 maker+maker:需要 maker fee <= {need_maker:.2f}bp(当前 {OKX_MAKER_BP:.1f}bp)才可能过 {LAGFOLLOW_BP:.1f}bp 位移。")
    print(f"  → 即需要负 maker(返佣)或 maker≈0 档;单靠 VIP 到 {OKX_MAKER_BP:.1f}bp→~1bp 仍不足。")
    print("\n结论:当前实测/公开费率往返成本均 >> lag-follow 位移;仅在 maker+maker 且接近零/返佣 maker 费率下才有希望,且依赖 maker 出场能成交。")
    print("约束:本脚本只读,不买币、不开抵扣、不改资金。VIP/OKB 抵扣实时状态 = NEED_API(需人工网页或后续只读账户 API)。")


if __name__ == "__main__":
    main()
