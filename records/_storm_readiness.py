#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""风暴日就位清单(2026-07-10 用户令三;只读+留痕)。

宗旨:casc触发/带pin/cap钉死/奖品密度全由同一种天气(风暴=全市场爆仓率高)驱动——
风暴是发薪日。本脚本每次调用核一遍四项就位度,风暴态自动留痕;平静期零事件不算异常。
四项:①采集器活性 ②探针武装状态 ③预算余量 ④对账通道。
用法:python3 _storm_readiness.py(可挂 cron/watch;当前手动+关键时点调用)。
"""
import json, os, subprocess, time
from datetime import datetime, timezone

REC = "/root/lingxi/records"
OKD = f"{REC}/okx_directional"
LOG = f"{REC}/_storm_readiness.jsonl"

def fuel_now():
    """近5min全市场爆仓笔数(读 fast_liquidation_events 尾部)。"""
    try:
        with open(f"{OKD}/fast_liquidation_events.jsonl", "rb") as f:
            f.seek(0, 2); f.seek(max(0, f.tell() - 500_000))
            tail = f.read().decode(errors="ignore").split("\n")
        cut = time.time() - 300
        n = 0
        for line in tail:
            try:
                r = json.loads(line)
                t = datetime.strptime(r["recorded_at"][:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()
                if t >= cut:
                    n += 1
            except Exception:
                continue
        return n
    except Exception:
        return -1

def alive(pat):
    try:
        return bool(subprocess.check_output(["pgrep", "-f", pat]).strip())
    except Exception:
        return False

def probe_state():
    try:
        d = json.load(open(f"{OKD}/fast_probe_state.json"))
        out = {}
        for k, v in d.get("probes", {}).items():
            out[k] = {"trades": v.get("trades"), "realized": round(v.get("realized_usdt", 0), 4),
                      "level": v.get("level", 0), "stalled": v.get("stalled", False)}
        return out
    except Exception:
        return {}

def check():
    now = datetime.now(timezone.utc)
    fuel = fuel_now()
    state = "风暴" if fuel >= 40 else ("死水" if 0 <= fuel < 20 else ("中间" if fuel >= 20 else "未知"))
    rpt = probe_state()
    row = {
        "ts": now.isoformat(), "fuel_5m": fuel, "weather": state,
        "collectors": {  # ①采集器活性
            "rule_hole_watch": alive("_rule_hole_watch"),
            "band_pin": alive("_band_pin_collector"),
            "lingxi_engine": alive("lingxi") and os.path.exists(f"{REC}/.lingxi-live.lock") if True else False,
        },
        "probes": rpt,  # ②探针武装
        "budget_ok": all(not v.get("stalled") for v in rpt.values()) if rpt else None,  # ③预算余量
        "recon_channel": os.path.exists(f"{OKD}/fast_v2_settlements.jsonl"),  # ④对账通道
    }
    # 服务活性单独探(pgrep lingxi 可能匹配到脚本自身)
    try:
        row["collectors"]["lingxi_engine"] = subprocess.call(["systemctl", "is-active", "--quiet", "lingxi"]) == 0
    except Exception:
        pass
    ready = (row["collectors"]["rule_hole_watch"] and row["collectors"]["band_pin"]
             and row["collectors"]["lingxi_engine"] and row["recon_channel"])
    row["all_ready"] = ready
    # 留痕规则:风暴态 或 就位异常 才写盘+打印;平静期正常=静默(用户令:平静零事件不作异常)
    if state == "风暴" or not ready:
        with open(LOG, "a") as f:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    print(json.dumps(row, ensure_ascii=False, indent=1))
    return ready

if __name__ == "__main__":
    check()
