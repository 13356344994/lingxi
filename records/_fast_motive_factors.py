#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fast 层"动机因子"离线验证(只读,零学费,不碰实盘)。

背景(2026-07-03 与用户对齐的因子哲学):
  - 机械因子(点差/失衡/冲量)只描述行为,不解释动机;同维度因子相关,天花板锁死。
  - 可预测性来自"不得不"与"忍不住":心理锚点(整数关口)、群体拥挤、被迫行为。
  - 小资金的战场在长尾/股票 perp(机构进不来的池塘),因子验证优先聚焦该子池。

数据(全部已有,无幸存者偏差):
  - okx_directional/fast_executable_shadow_events.jsonl:每分钟全 universe 候选快照
    (score/side/reason/盘口),含未开仓候选。
  - okx_directional/fast_dynamic_feature_events.jsonl:tick 级 mid 序列(算前向走势)。

输出:
  1. scorer 校准曲线:score 档 → P(前向跟随) 是否单调(v2 368 笔上发现 >=0.95 档倒挂,此处大样本证实/证伪)。
  2. reason(足迹类型)→ 跟随率:冲量延续类是否系统性差(=我们自己在 FOMO)。
  3. 动机因子:
     a. 整数锚距离(心理锚):入场价距最近整数关口的 bps,近锚是否改变跟随率;
     b. 股票 perp / 长尾流动性子池 vs 主流:维度优势该在哪个池塘行使;
     c. 盘口深度档(池塘大小)× score 交叉。
跟随定义:信号方向 60s 内 MFE >= 6bps(与 v2 结算复盘同口径);同时报 30s 均值位移。
用法:python3 _fast_motive_factors.py [--max-cand N]
"""
import bisect
import json
import os
import sys
from collections import defaultdict
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
SHADOW = os.path.join(HERE, "okx_directional", "fast_executable_shadow_events.jsonl")
FEATURES = os.path.join(HERE, "okx_directional", "fast_dynamic_feature_events.jsonl")

# OKX 股票/ETF perp(xStocks 类,按已见 universe 手工维护;不在此表=按 crypto 处理)
STOCK_PERPS = {
    "SKHYNIX-USDT", "SAMSUNG-USDT", "SOXL-USDT", "SNDK-USDT", "AMZN-USDT",
    "CRWV-USDT", "AAOI-USDT", "MRVL-USDT", "CRCL-USDT", "TTMI-USDT",
    "ALAB-USDT", "AAPL-USDT", "TSLA-USDT", "NVDA-USDT", "MSTR-USDT",
    "COIN-USDT", "HOOD-USDT", "GOOGL-USDT", "META-USDT", "OPEN-USDT",
    "RGTI-USDT", "IREN-USDT", "PLTR-USDT", "QQQ-USDT", "SPY-USDT",
}

FOLLOW_BPS = 6.0      # 跟随判定阈值(与 v2 复盘同口径)
HORIZON_S = 60.0      # 前向窗口
MID_MAX_GAP_S = 5.0   # 候选时刻与最近 tick 的最大允许间隔


def parse_ts(s):
    # "2026-07-02T17:49:26.096271787Z" → epoch 秒;py3.6 fromisoformat 不可用且纳秒超长,手工截断
    s = s.rstrip("Z")
    head, _, frac = s.partition(".")
    dt = datetime.strptime(head, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    t = dt.timestamp()
    if frac:
        t += float("0." + frac[:9])
    return t


def extract_field(line, key):
    # 快速字符串抽取(1.2GB 流逐行 json.loads 太慢):返回 key 的原始值串或 None。
    tag = '"%s":' % key
    i = line.find(tag)
    if i < 0:
        return None
    j = i + len(tag)
    if line[j] == '"':
        k = line.find('"', j + 1)
        return line[j + 1:k]
    k = j
    n = len(line)
    while k < n and line[k] not in ",}":
        k += 1
    return line[j:k]


def load_candidates():
    cands = []
    with open(SHADOW) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            side = d.get("fast_candidate_side")
            if side not in ("long", "short"):
                continue
            mid = d.get("mid") or 0
            if mid <= 0:
                continue
            cands.append({
                "ts": parse_ts(d["recorded_at"]),
                "sym": d["symbol"],
                "side": side,
                "score": float(d.get("fast_candidate_score") or 0),
                "reason": d.get("fast_candidate_reason") or "?",
                "mid": float(mid),
                "spread": float(d.get("spread_bps") or 0),
                "depth10": float(d.get("depth_notional_10bps") or 0),
            })
    return cands


def load_mid_series(symbols):
    series = defaultdict(lambda: ([], []))  # sym -> (ts_list, mid_list)
    with open(FEATURES) as f:
        for line in f:
            sym = extract_field(line, "symbol")
            if sym not in symbols:
                continue
            ts_raw = extract_field(line, "recorded_at")
            mid_raw = extract_field(line, "mid")
            if not ts_raw or not mid_raw:
                continue
            try:
                ts = parse_ts(ts_raw)
                mid = float(mid_raw)
            except Exception:
                continue
            if mid <= 0:
                continue
            ts_l, mid_l = series[sym]
            ts_l.append(ts)
            mid_l.append(mid)
    return series


def forward_metrics(series, sym, ts, mid0, side):
    """返回 (mfe60_bps, ret30_bps) 或 None(数据缺口)。"""
    pair = series.get(sym)
    if not pair:
        return None
    ts_l, mid_l = pair
    i = bisect.bisect_left(ts_l, ts)
    if i >= len(ts_l) or ts_l[i] - ts > MID_MAX_GAP_S:
        return None
    sign = 1.0 if side == "long" else -1.0
    mfe = 0.0
    ret30 = None
    last30 = None
    j = i
    n = len(ts_l)
    while j < n and ts_l[j] - ts <= HORIZON_S:
        bps = sign * (mid_l[j] - mid0) / mid0 * 1e4
        if bps > mfe:
            mfe = bps
        if ts_l[j] - ts <= 30.0:
            last30 = bps
        j += 1
    if j - i < 3:  # 窗口内 tick 太少=数据缺口
        return None
    ret30 = last30 if last30 is not None else 0.0
    return mfe, ret30


def anchor_distance_bps(price):
    """距最近"整数锚"的 bps。锚 = 1/2/5 × 10^k 网格(人类记忆的圆整位)。"""
    import math
    if price <= 0:
        return 1e9
    exp = math.floor(math.log10(price))
    best = 1e9
    for k in (exp - 1, exp, exp + 1):
        step = 10.0 ** k
        for m in (1.0, 2.0, 2.5, 5.0):
            unit = m * step
            level = round(price / unit) * unit
            if level > 0:
                d = abs(price - level) / price * 1e4
                if d < best:
                    best = d
    return best


def bucket_table(title, rows, keyfn, min_n=50):
    groups = defaultdict(list)
    for r in rows:
        groups[keyfn(r)].append(r)
    print("\n--- %s ---" % title)
    print("%-28s %6s %10s %10s %10s" % ("桶", "n", "P(跟随)", "MFE60均", "ret30均"))
    for k in sorted(groups, key=str):
        g = groups[k]
        if len(g) < min_n:
            continue
        pf = sum(1 for r in g if r["mfe60"] >= FOLLOW_BPS) / len(g)
        m = sum(r["mfe60"] for r in g) / len(g)
        r30 = sum(r["ret30"] for r in g) / len(g)
        print("%-28s %6d %9.1f%% %9.2f %9.2f" % (str(k)[:28], len(g), pf * 100, m, r30))


def main():
    max_cand = 0
    if "--max-cand" in sys.argv:
        max_cand = int(sys.argv[sys.argv.index("--max-cand") + 1])
    print("[1/3] 读取 shadow 候选 …")
    cands = load_candidates()
    if max_cand:
        cands = cands[:max_cand]
    syms = set(c["sym"] for c in cands)
    print("  方向性候选 %d 条,涉及 %d symbol" % (len(cands), len(syms)))
    print("[2/3] 流式扫描 tick mid 序列(1.2GB,约 2-4 分钟)…")
    series = load_mid_series(syms)
    total_ticks = sum(len(v[0]) for v in series.values())
    print("  载入 %d symbol、%d ticks" % (len(series), total_ticks))
    print("[3/3] 计算前向跟随 …")
    rows = []
    skipped = 0
    for c in cands:
        fm = forward_metrics(series, c["sym"], c["ts"], c["mid"], c["side"])
        if fm is None:
            skipped += 1
            continue
        c["mfe60"], c["ret30"] = fm
        c["anchor"] = anchor_distance_bps(c["mid"])
        rows.append(c)
    print("  有效样本 %d(数据缺口跳过 %d)" % (len(rows), skipped))
    if not rows:
        return
    base = sum(1 for r in rows if r["mfe60"] >= FOLLOW_BPS) / len(rows)
    print("\n基线 P(60s 跟随>=%.0fbps) = %.1f%%(全部方向性候选)" % (FOLLOW_BPS, base * 100))

    bucket_table("① scorer 校准(score 档)", rows, lambda r:
                 "a<0.75" if r["score"] < 0.75 else
                 "b0.75-0.85" if r["score"] < 0.85 else
                 "c0.85-0.90" if r["score"] < 0.90 else
                 "d0.90-0.95" if r["score"] < 0.95 else "e>=0.95")
    bucket_table("② 足迹类型(reason,前3段)", rows,
                 lambda r: "|".join(r["reason"].split("|")[:3])[:28], min_n=200)
    bucket_table("③ 心理锚:距整数关口", rows, lambda r:
                 "a贴锚<3bp" if r["anchor"] < 3 else
                 "b近锚3-10bp" if r["anchor"] < 10 else
                 "c中10-30bp" if r["anchor"] < 30 else "d远>=30bp")
    bucket_table("④ 池塘:股票perp vs crypto", rows,
                 lambda r: "stock_perp" if r["sym"] in STOCK_PERPS else "crypto")
    bucket_table("⑤ 池塘深度(10bp内挂单)", rows, lambda r:
                 "a薄<5k" if r["depth10"] < 5000 else
                 "b中5-20k" if r["depth10"] < 20000 else
                 "c厚20-60k" if r["depth10"] < 60000 else "d很厚>=60k")
    # ⑥ 高分子集内做正交检验:动机因子在 score>=0.9 内是否仍分层
    hi = [r for r in rows if r["score"] >= 0.90]
    if len(hi) >= 200:
        bucket_table("⑥ score>=0.90 内:锚距分层(正交性)", hi, lambda r:
                     "a贴锚<3bp" if r["anchor"] < 3 else
                     "b近锚3-10bp" if r["anchor"] < 10 else "c远>=10bp")
        bucket_table("⑦ score>=0.90 内:池塘分层", hi,
                     lambda r: "stock_perp" if r["sym"] in STOCK_PERPS else "crypto")


if __name__ == "__main__":
    main()
