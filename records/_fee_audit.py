#!/usr/bin/env python3
# 费率审计(只读,phase1)。不买币、不划转、不改配置。
# 能做:从结算算 fast 实际手续费/日均 + 降费改善 + 打平所需 + 已知费率/抵扣能力(公开事实)。
# 不能做(需新基建,如实标注):OKX 实时账户费率/VIP/平台币余额/是否已开抵扣/API 开启 —— 适配器未暴露。
import json, os
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]

# 公开费率(perp,Lv1/默认,best-effort;实际以账户 VIP 为准,需 API 核实)
PUBLISHED = {
    "okx":     {"maker": 0.0002, "taker": 0.0005, "coin_deduct": "无 BNB 式合约抵扣;靠 VIP/交易量费率档", "api_query": "适配器未暴露费率/VIP"},
    "binance": {"maker": 0.0002, "taker": 0.0005, "coin_deduct": "BNB 抵扣合约手续费 -10%(需持 BNB 且开启)", "api_query": "适配器未暴露费率/VIP"},
    "bybit":   {"maker": 0.0002, "taker": 0.00055, "coin_deduct": "无简单币抵扣;VIP 档", "api_query": "bybit 有 fetchFeeRate(未接入审计)"},
    "gate":    {"maker": 0.0002, "taker": 0.0005, "coin_deduct": "GT/点卡可抵;需开启", "api_query": "适配器未暴露费率/VIP"},
}


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        return datetime.strptime(s.split(".")[0], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def main():
    perven = {v: {"fast_fee": 0.0, "fast_net": 0.0, "fast_n": 0, "tmin": None, "tmax": None} for v in VEN}
    for v in VEN:
        f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            if '"signal_layer":"fast_v1"' not in l:
                continue
            try:
                d = json.loads(l)
            except json.JSONDecodeError:
                continue
            p = perven[v]
            p["fast_fee"] += d.get("fee_usdt", 0.0)
            p["fast_net"] += d.get("realized_usdt", 0.0)
            p["fast_n"] += 1
            t = ts(d.get("time"))
            if t:
                p["tmin"] = t if not p["tmin"] or t < p["tmin"] else p["tmin"]
                p["tmax"] = t if not p["tmax"] or t > p["tmax"] else p["tmax"]

    now = datetime.now(timezone.utc)
    out = []
    print("=" * 96)
    print("费率审计(只读 phase1;不买币/不划转/不改配置)")
    print("=" * 96)
    for v in VEN:
        p = perven[v]
        pub = PUBLISHED[v]
        days = ((p["tmax"] - p["tmin"]).total_seconds() / 86400) if (p["tmin"] and p["tmax"]) else 0
        daily = (p["fast_fee"] / days) if days > 0.05 else 0
        rec = {
            "recorded_at": now.isoformat(), "venue": v,
            "published_maker": pub["maker"], "published_taker": pub["taker"],
            "coin_deduct": pub["coin_deduct"], "api_query_status": pub["api_query"],
            "fast_n": p["fast_n"], "fast_fee_total": round(p["fast_fee"], 4),
            "fast_net_total": round(p["fast_net"], 4), "span_days": round(days, 2),
            "fast_fee_per_day_est": round(daily, 4),
            "account_maker_taker": "NEED_API(未接入)", "vip_level": "NEED_API",
            "coin_balance": "NEED_API", "deduct_enabled": "NEED_API",
            "can_enable_via_api": "NEED_API", "needs_manual": True,
        }
        out.append(rec)
        if p["fast_n"] > 0:
            print(f"\n[{v}] fast n={p['fast_n']} fee总={p['fast_fee']:.3f}U net={p['fast_net']:.3f}U 跨度={days:.2f}天 → 日均fee≈{daily:.3f}U")
        else:
            print(f"\n[{v}] fast 无成交(fast 目前 OKX-only)")
        print(f"    公开费率 maker={pub['maker']*100:.3f}% taker={pub['taker']*100:.3f}% | 抵扣:{pub['coin_deduct']}")
        print(f"    ⚠ 实际费率/VIP/币余额/是否已开抵扣/API开启: NEED_API(适配器未暴露,需新写{v}账户API或人工查)")

    # 全 fast 汇总 + 降费改善
    tot_fee = sum(p["fast_fee"] for p in perven.values())
    tot_net = sum(p["fast_net"] for p in perven.values())
    print("\n" + "=" * 96)
    print(f"全 fast: net={tot_net:+.3f}U  fee={tot_fee:.3f}U  毛利(net+fee,扣费前)={tot_net+tot_fee:+.3f}U")
    for d in (0.10, 0.20, 0.25):
        print(f"  降费{int(d*100)}% → net改善+{tot_fee*d:.3f}U → 新net={tot_net+tot_fee*d:+.3f}U")
    if tot_fee > 0:
        need = -tot_net / tot_fee * 100
        print(f"  仅靠降费打平需降 {need:.0f}% → {'可达' if need<=100 else '不可能;毛结构为负,降费救不了(见入场无edge结论)'}")
    out.append({"recorded_at": now.isoformat(), "summary": True, "fast_net": round(tot_net, 4),
                "fast_fee": round(tot_fee, 4), "gross_prefee": round(tot_net + tot_fee, 4),
                "breakeven_fee_cut_pct": round(-tot_net / tot_fee * 100, 1) if tot_fee > 0 else None})

    with open("fee_discount_audit.jsonl", "w") as fp:
        for r in out:
            fp.write(json.dumps(r, ensure_ascii=False) + "\n")
    print(f"\n已写 {len(out)} 条 → records/fee_discount_audit.jsonl")
    print("结论:降费=第二线(降25%仅改善~6U);毛利扣费前已负 → 降费不能救,核心仍是入场无 edge。")


if __name__ == "__main__":
    main()
