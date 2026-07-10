#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""限价带 pin 采集器(2026-07-10;规则洞②数据地板;nohup 常驻,只读)。

X/Y/Z 参数官方不公示,只能实测:每 30s 对"近 30min 暴动币"(census 尾部)轮询
/public/price-limit + ticker,价格进带沿 0.5% 以内即记 pin 事件行(含带宽=buyLmt/sellLmt
相对 mid 的百分比=Z 的实测值);解锁(离带)也记。落盘 band_pin_events.jsonl。
限频:≤3 币/轮×2 请求,礼让。
"""
import json, time, urllib.request, bisect
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
OUT = f"{REC}/band_pin_events.jsonl"
UA = {"User-Agent": "Mozilla/5.0"}

def get(url):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())

def hot_syms(n=6):
    """census 尾部近 30min 暴动币(读文件尾 200KB)。"""
    try:
        with open(f"{REC}/fast_mover_census.jsonl", "rb") as f:
            f.seek(0, 2)
            f.seek(max(0, f.tell() - 200_000))
            tail = f.read().decode(errors="ignore").split("\n")
        cut = time.time() - 1800
        syms = {}
        for line in tail:
            try:
                r = json.loads(line)
                t = datetime.strptime(r["recorded_at"][:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()
                if t >= cut:
                    syms[r["symbol"]] = syms.get(r["symbol"], 0) + 1
            except Exception:
                continue
        return [s for s, _ in sorted(syms.items(), key=lambda kv: -kv[1])[:n]]
    except Exception:
        return []

state = {}  # inst -> pinned side
print(f"[{datetime.now(timezone.utc):%H:%M}] 限价带采集器上岗", flush=True)
while True:
    for sym in hot_syms():
        inst = sym + "-SWAP"
        try:
            pl = get(f"https://www.okx.com/api/v5/public/price-limit?instId={inst}")["data"][0]
            tk = get(f"https://www.okx.com/api/v5/market/ticker?instId={inst}")["data"][0]
            buy, sell = float(pl["buyLmt"]), float(pl["sellLmt"])
            last = float(tk["last"])
            if last <= 0 or buy <= 0:
                continue
            mid_band = (buy + sell) / 2
            width_up = (buy / last - 1) * 100
            width_dn = (1 - sell / last) * 100
            side = "up" if width_up < 0.5 else ("dn" if width_dn < 0.5 else "")
            prev = state.get(inst, "")
            if side != prev:
                row = dict(ts=datetime.now(timezone.utc).isoformat(), inst=inst, event=("pin_" + side if side else "unpin_" + prev),
                           last=last, buy_lmt=buy, sell_lmt=sell,
                           z_up_pct=round(width_up, 3), z_dn_pct=round(width_dn, 3))
                with open(OUT, "a") as f:
                    f.write(json.dumps(row) + "\n")
                print(f"[{row['ts'][11:16]}] {row['event']} {inst} last={last} 带宽 +{width_up:.2f}%/-{width_dn:.2f}%", flush=True)
                state[inst] = side
        except Exception:
            continue
        time.sleep(0.7)
    time.sleep(30)
