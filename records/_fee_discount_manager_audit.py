#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# ============================================================================
# fee discount manager —— 只读审计脚本(phase1)
# ----------------------------------------------------------------------------
# 目标:为高频 fast scalper 做手续费折扣/VIP/平台币抵扣的【只读审计/估算/建议】。
#
# 【纪律 — 本脚本严格遵守】
#   - 只读:不买币(OKB/BNB/GT)、不划转、不开启抵扣、不卖币、不改任何交易参数/config。
#   - 本脚本【无任何网络调用】,也不读取/打印任何 API key,零资金风险、零交易主流程接触。
#   - 只做:从 settlements 算 fast 实际手续费/日均 + 降费节省估算;公开费率作 maker/taker 缺省
#     (标注 "published, 非账户实时");账户级实时项(费率/VIP/余额/是否已开抵扣/API 能否开启)
#     一律标 NEED_API / MANUAL_REQUIRED,【绝不编造费率/VIP/余额】。
#   - 每所独立 try/except:单所失败不影响其他;异常写 audit(venue/error/stage/timestamp)。
#   - Python 3.6:不用 f-string 之外的新特性,不用 subprocess capture_output(本脚本无 subprocess)。
#
# 【差异化(按各所真实情况,不套假通用逻辑)】
#   OKX     : discount_asset=OKB。OKB 不一定像 BNB 直接抵扣合约手续费,可能只是 VIP 量级档 →
#             discount_supported=NEED_API(存疑,需人工网页/账户 API 确认)。fast 目前 OKX-only,
#             故 OKX 的 estimated_daily_fee / 节省是从真实 settlements 算出的。
#   Binance : discount_asset=BNB。BNB 抵扣合约手续费 -10% 机制明确 → discount_supported=true;
#             但账户是否已开启(bnbBurn)= NEED_API。fast 无成交。
#   Bybit   : 无明确平台币抵扣机制 → discount_supported=false / discount_asset=unsupported。
#             账户费率虽可经私有 API 拉取(venue/bybit fetchFeeRate),但本脚本不做网络调用 → NEED_API。
#   Gate    : discount_asset=GT。GT 可抵手续费 → discount_supported=true;账户级是否开启 = NEED_API。
#
# 输出:records/fee_discount_audit.jsonl(每所一条 + 末尾一条汇总)。
# ============================================================================
import json
import os
from datetime import datetime, timezone

OUT_FILE = "fee_discount_audit.jsonl"

# 公开费率(perp,默认档 best-effort;实际以账户 VIP/抵扣为准 → 标 published,非账户实时)
# discount_supported 语义:该所是否存在"平台币直接抵扣手续费"机制(事实层),不代表账户已开启。
VENUES = {
    "okx": {
        "settlements": "okx_directional/settlements.jsonl",
        "pub_maker": 0.0002, "pub_taker": 0.0005,
        "discount_asset": "OKB",
        # OKB 抵扣存疑:不一定像 BNB 直接抵合约,可能只是 VIP 量级 → 标 NEED_API
        "discount_supported": "NEED_API",
        "note": ("OKB 不一定直接抵扣合约手续费,可能仅体现为 VIP 量级费率档;"
                 "需人工网页(OKX 费率页)或新增 OKX 账户 API 确认。"),
    },
    "binance": {
        "settlements": "binance_directional/settlements.jsonl",
        "pub_maker": 0.0002, "pub_taker": 0.0005,
        "discount_asset": "BNB",
        "discount_supported": True,  # BNB 抵扣合约手续费 -10% 机制明确存在
        "note": ("BNB 抵扣合约手续费 -10% 机制存在;账户是否已开启(bnbBurn)= NEED_API。"),
    },
    "bybit": {
        "settlements": "bybit_directional/settlements.jsonl",
        "pub_maker": 0.0002, "pub_taker": 0.00055,
        "discount_asset": "unsupported",  # 无明确平台币抵扣机制
        "discount_supported": False,
        "note": ("Bybit 无明确平台币直接抵扣手续费机制(仅 VIP 档);"
                 "账户费率可经私有 API 拉取,但本脚本不做网络调用 → NEED_API。"),
    },
    "gate": {
        "settlements": "gate_directional/settlements.jsonl",
        "pub_maker": 0.0002, "pub_taker": 0.0005,
        "discount_asset": "GT",
        "discount_supported": True,  # GT/点卡可抵手续费
        "note": ("GT 可抵扣手续费机制存在;账户级是否开启 = NEED_API。"),
    },
}

VENUE_ORDER = ["okx", "binance", "bybit", "gate"]


def parse_ts(s):
    """解析 settlements 里的 RFC3339 时间戳(容错)。"""
    s = (s or "").rstrip("Z")
    if not s:
        return None
    core = s.split(".")[0]
    try:
        return datetime.strptime(core, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def scan_fast(settlements_path):
    """从 settlements.jsonl 累计 fast_v1 的真实 fee/net/条数/时间跨度。只读文件,不改。"""
    agg = {"n": 0, "fee": 0.0, "net": 0.0, "tmin": None, "tmax": None}
    if not os.path.exists(settlements_path):
        return agg, "settlements 文件不存在(该所无 fast 记录)"
    with open(settlements_path, "r") as fp:
        for line in fp:
            # 快速预筛,避免无谓 json.loads
            if '"signal_layer":"fast_v1"' not in line:
                continue
            try:
                d = json.loads(line)
            except (ValueError, json.JSONDecodeError):
                continue
            if d.get("signal_layer") != "fast_v1":
                continue
            agg["n"] += 1
            agg["fee"] += float(d.get("fee_usdt", 0.0) or 0.0)
            agg["net"] += float(d.get("realized_usdt", 0.0) or 0.0)
            t = parse_ts(d.get("time"))
            if t is not None:
                if agg["tmin"] is None or t < agg["tmin"]:
                    agg["tmin"] = t
                if agg["tmax"] is None or t > agg["tmax"]:
                    agg["tmax"] = t
    return agg, None


def build_venue_record(now_iso, venue):
    """单所审计记录(严格字段)。任何异常由调用方 try/except 捕获转 ERROR 记录。"""
    cfg = VENUES[venue]
    agg, scan_note = scan_fast(cfg["settlements"])

    days = 0.0
    if agg["tmin"] is not None and agg["tmax"] is not None:
        days = (agg["tmax"] - agg["tmin"]).total_seconds() / 86400.0
    daily_fee = (agg["fee"] / days) if days > 0.05 else 0.0

    have_fast = agg["n"] > 0
    # 状态:Bybit 无平台币抵扣 → UNSUPPORTED;其余账户级实时项拿不到 → NEED_API。
    if cfg["discount_supported"] is False:
        status = "UNSUPPORTED"
    else:
        status = "NEED_API"

    # 建议:能算的算,拿不到的说明需人工/需账户 API。
    if venue == "okx":
        action = ("已从真实 settlements 估算 fast 日均 fee 与降费节省(见下)。OKB 抵扣性质存疑:"
                  "请人工在 OKX 网页费率页确认是否/如何抵扣合约手续费,或后续新增 OKX 账户级 "
                  "fee-rate/VIP/OKB 余额只读 API 后再判定是否值得开启。切勿在未确认前自动买 OKB。")
    elif venue == "binance":
        action = ("BNB -10% 抵扣机制明确;当前 fast 无 Binance 成交,节省估算为 0。"
                  "账户是否已开启 BNB 抵扣(bnbBurn)需人工网页确认或经账户 API 只读查询。")
    elif venue == "bybit":
        action = ("Bybit 无平台币直接抵扣;费率优化只能走 VIP 量级档。无自动动作建议。"
                  "如需账户实时费率,可后续接入 bybit 私有 fetchFeeRate(只读)。")
    else:  # gate
        action = ("GT 可抵扣;当前 fast 无 Gate 成交,节省估算为 0。"
                  "账户级 GT 抵扣是否开启需人工网页确认或经账户 API(/wallet/fee gt_discount)只读查询。")

    risk = ("maker/taker 为 published 公开费率(非账户实时);vip_level / discount_asset_balance / "
            "discount_enabled / api_can_enable_discount 均为账户级实时数据,本只读脚本无网络调用故无法获取 → "
            "标 NEED_API,需人工网页确认或后续新增该所账户级只读 API。") + cfg["note"]
    if scan_note:
        risk = risk + " [" + scan_note + "]"

    rec = {
        "time": now_iso,
        "venue": venue,
        "status": status,
        "maker_fee": cfg["pub_maker"],       # published,非账户实时
        "taker_fee": cfg["pub_taker"],       # published,非账户实时
        "vip_level": "NEED_API",
        "discount_asset": cfg["discount_asset"],
        "discount_asset_balance": "NEED_API",
        "discount_asset_value_usdt": "NEED_API",
        "discount_enabled": ("UNSUPPORTED" if cfg["discount_supported"] is False else "NEED_API"),
        "discount_supported": cfg["discount_supported"],
        "api_can_enable_discount": ("UNSUPPORTED" if cfg["discount_supported"] is False else "NEED_API"),
        "estimated_daily_fee_usdt": round(daily_fee, 6),
        "estimated_saving_10pct": round(daily_fee * 0.10, 6),
        "estimated_saving_20pct": round(daily_fee * 0.20, 6),
        "estimated_saving_25pct": round(daily_fee * 0.25, 6),
        "action_recommendation": action,
        "risk_note": risk,
        "error": None,
        # 附带只读计算明细(非规定字段,便于人工核对;不影响主字段)
        "_fast_n": agg["n"],
        "_fast_fee_total": round(agg["fee"], 6),
        "_fast_net_total": round(agg["net"], 6),
        "_span_days": round(days, 3),
        "_maker_taker_source": "published (非账户实时)",
    }
    return rec, have_fast, daily_fee, agg


def main():
    now = datetime.now(timezone.utc)
    now_iso = now.isoformat()
    records = []
    tot_daily = 0.0

    print("=" * 96)
    print("fee discount manager 审计(只读 phase1;无网络调用/不买币/不划转/不改配置)")
    print("=" * 96)

    for venue in VENUE_ORDER:
        stage = "init"
        try:
            stage = "scan+build"
            rec, have_fast, daily_fee, agg = build_venue_record(now_iso, venue)
            records.append(rec)
            tot_daily += daily_fee

            print("\n[{}] status={} discount_asset={} discount_supported={}".format(
                venue, rec["status"], rec["discount_asset"], rec["discount_supported"]))
            if have_fast:
                print("    fast n={} fee总={:.4f}U net={:+.4f}U 跨度={:.2f}天 -> 日均fee≈{:.4f}U".format(
                    agg["n"], agg["fee"], agg["net"], rec["_span_days"], daily_fee))
                print("    降费节省估算/日: 10%=+{:.4f}U  20%=+{:.4f}U  25%=+{:.4f}U".format(
                    rec["estimated_saving_10pct"], rec["estimated_saving_20pct"], rec["estimated_saving_25pct"]))
            else:
                print("    fast 无成交(fast 目前 OKX-only)-> 节省估算=0")
            print("    published maker={:.4f}% taker={:.4f}% (非账户实时)".format(
                rec["maker_fee"] * 100, rec["taker_fee"] * 100))
            print("    账户实时(费率/VIP/余额/是否开抵扣/API能否开启): NEED_API / {}".format(
                "UNSUPPORTED" if rec["discount_supported"] is False else "NEED_API"))
        except Exception as e:  # 单所失败不影响其他 -> 写 ERROR 记录
            err_rec = {
                "time": now_iso, "venue": venue, "status": "ERROR",
                "maker_fee": None, "taker_fee": None, "vip_level": None,
                "discount_asset": None, "discount_asset_balance": None,
                "discount_asset_value_usdt": None, "discount_enabled": None,
                "discount_supported": None, "api_can_enable_discount": None,
                "estimated_daily_fee_usdt": None, "estimated_saving_10pct": None,
                "estimated_saving_20pct": None, "estimated_saving_25pct": None,
                "action_recommendation": "该所审计异常,已隔离;不影响其他所与交易主流程。",
                "risk_note": "stage=" + stage,
                "error": "{}: {}".format(type(e).__name__, e),
            }
            records.append(err_rec)
            print("\n[{}] ERROR stage={} err={}: {}".format(venue, stage, type(e).__name__, e))

    # 汇总(只统计有数据的所;当前 fast=OKX-only)
    summary = {
        "time": now_iso,
        "venue": "_summary",
        "status": "OK",
        "fast_scope": "OKX-only(current)",
        "estimated_daily_fee_usdt_all": round(tot_daily, 6),
        "estimated_saving_10pct_all": round(tot_daily * 0.10, 6),
        "estimated_saving_20pct_all": round(tot_daily * 0.20, 6),
        "estimated_saving_25pct_all": round(tot_daily * 0.25, 6),
        "note": ("降费=第二线优化;账户级实时费率/VIP/抵扣状态均 NEED_API,需人工网页确认或"
                 "后续新增各所账户级只读 API;本阶段严禁任何自动动作(买币/开抵扣)。"),
        "error": None,
    }
    records.append(summary)

    print("\n" + "=" * 96)
    print("汇总: 全 fast 日均 fee≈{:.4f}U -> 降费节省/日 10%=+{:.4f}U 20%=+{:.4f}U 25%=+{:.4f}U".format(
        tot_daily, tot_daily * 0.10, tot_daily * 0.20, tot_daily * 0.25))
    print("账户级实时项(费率/VIP/余额/是否开抵扣/API能否开启)全部 NEED_API;Bybit 抵扣 UNSUPPORTED。")

    # 覆盖写 audit(records 运行数据,不 commit)
    with open(OUT_FILE, "w") as fp:
        for r in records:
            fp.write(json.dumps(r, ensure_ascii=False) + "\n")
    print("\n已写 {} 条 -> records/{}".format(len(records), OUT_FILE))
    print("提示:本文件为运行态审计数据,请勿 git commit。")


if __name__ == "__main__":
    main()
