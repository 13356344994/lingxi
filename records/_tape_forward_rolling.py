#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tape_burst 滚动前向复测(只读;07-05 接管 agent 建,回应用户"不许等周一"纠偏)。

口径与离线研究对齐:burst 探测事件 → +1s 进场(hot_mids 首个 ≥t0+1s 的 mid)→
60s 前向毛收益(顺 burst 方向,bps)。分桶:燃料 regime(<20/20-40/≥40)×
官方爆仓确认(burst 后 ≤3s 内本币 OKX 爆仓推送到达 = tape_burst 整枪的实际开火漏斗)。

预注册裁决锚(接手文档):活跃 regime 下确认桶 60s 中位站住 +13.6bps 量级 → 向用户提探针。
样本充分性由机制分辨率定,不由钟表定:本脚本可随时重跑,n 到了就出裁决。
"""
import json
import sys
import calendar
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ts_epoch(s):
    """'2026-07-05T03:15:48.48253096Z' -> epoch 秒(float)。py3.6 无 fromisoformat。"""
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    frac = 0.0
    if len(s) > 20 and s[19] == ".":
        digits = s[20:].rstrip("Z")
        try:
            frac = float("0." + digits)
        except ValueError:
            frac = 0.0
    return base + frac


def load_bursts():
    out = []
    with open(REC + "/fast_burst_detect.jsonl") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except ValueError:
                continue
            out.append({
                "t0": ts_epoch(r["recorded_at"]),
                "sym": r["symbol"],
                "dir": 1.0 if r.get("side") == "buy" else -1.0,
                "fuel": float(r.get("market_liq_5m", 0) or 0),
                "entry": None, "exit": None,
            })
    out.sort(key=lambda e: e["t0"])
    return out


def load_liq_times():
    """OKX 官方爆仓推送时刻(recorded_at 即本地收到时刻,与实盘确认口径一致)。"""
    per = {}
    with open(REC + "/fast_liquidation_events.jsonl") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except ValueError:
                continue
            per.setdefault(r["symbol"], []).append(ts_epoch(r["recorded_at"]))
    for v in per.values():
        v.sort()
    return per


def mark_confirmed(events, liq_times, window=3.0):
    import bisect
    for e in events:
        ts = liq_times.get(e["sym"])
        e["confirmed"] = False
        if not ts:
            continue
        i = bisect.bisect_right(ts, e["t0"])
        if i < len(ts) and ts[i] - e["t0"] <= window:
            e["confirmed"] = True


def fill_paths(events):
    """单遍流式扫 hot_mids:entry=首个 ≥t0+1s(容忍到 t0+6s),exit=首个 ≥t0+61s(容忍到 t0+70s)。"""
    if not events:
        return
    entry_idx, exit_idx = {}, {}
    for k, e in enumerate(events):
        for s in range(int(e["t0"] + 1), int(e["t0"] + 7)):
            entry_idx.setdefault(s, []).append(k)
        for s in range(int(e["t0"] + 61), int(e["t0"] + 71)):
            exit_idx.setdefault(s, []).append(k)
    tmin_prefix = datetime.utcfromtimestamp(events[0]["t0"]).strftime("%Y-%m-%dT%H:%M:%S")
    with open(REC + "/fast_hot_mids.jsonl") as f:
        for line in f:
            # 廉价预筛:recorded_at 在行首,ISO 字符串可字典序比较,免 262MB 全量 json 解析
            p = line.find('"recorded_at":"')
            if p < 0:
                continue
            tstr = line[p + 15: p + 34]
            if tstr < tmin_prefix:
                continue
            sec = calendar.timegm(datetime.strptime(tstr, "%Y-%m-%dT%H:%M:%S").timetuple())
            ks = entry_idx.get(sec, []) + exit_idx.get(sec, [])
            if not ks:
                continue
            try:
                r = json.loads(line)
            except ValueError:
                continue
            mids = r.get("mids", {})
            T = ts_epoch(r["recorded_at"])
            for k in ks:
                e = events[k]
                m = mids.get(e["sym"])
                if m is None or m <= 0:
                    continue
                if e["entry"] is None and e["t0"] + 1 <= T <= e["t0"] + 6:
                    e["entry"] = m
                if e["exit"] is None and e["t0"] + 61 <= T <= e["t0"] + 70:
                    e["exit"] = m


def bucket_fuel(f):
    if f >= 40:
        return "活跃(>=40)"
    if f >= 20:
        return "中间(20-40)"
    return "死水(<20)"


def stats(rows):
    if not rows:
        return "n=0"
    med = statistics.median(rows)
    mean = sum(rows) / len(rows)
    win = 100.0 * sum(1 for x in rows if x > 0) / len(rows)
    return "n=%d 中位=%+.1fbps 均值=%+.1f 顺向=%.0f%%" % (len(rows), med, mean, win)


def main():
    events = load_bursts()
    mark_confirmed(events, load_liq_times())
    fill_paths(events)
    done = [e for e in events if e["entry"] and e["exit"]]
    for e in done:
        e["ret"] = e["dir"] * (e["exit"] - e["entry"]) / e["entry"] * 1e4
    print("burst 事件=%d | 有完整 +1s/+61s 路径=%d | 官方≤3s确认=%d" %
          (len(events), len(done), sum(1 for e in events if e["confirmed"])))
    print("\n== 全部(毛,60s 顺 burst 方向)==")
    print(" ", stats([e["ret"] for e in done]))
    print("\n== 按燃料 regime ==")
    for b in ("死水(<20)", "中间(20-40)", "活跃(>=40)"):
        print("  %-12s %s" % (b, stats([e["ret"] for e in done if bucket_fuel(e["fuel"]) == b])))
    print("\n== 官方确认漏斗(=整枪实际开火口径)==")
    for flag, name in ((True, "确认桶"), (False, "未确认(纯大单)")):
        print("  %-12s %s" % (name, stats([e["ret"] for e in done if e["confirmed"] == flag])))
    print("\n== 确认×燃料(裁决锚:活跃×确认 中位 vs +13.6)==")
    for b in ("死水(<20)", "中间(20-40)", "活跃(>=40)"):
        rows = [e["ret"] for e in done if e["confirmed"] and bucket_fuel(e["fuel"]) == b]
        print("  确认×%-12s %s" % (b, stats(rows)))


if __name__ == "__main__":
    sys.exit(main())
